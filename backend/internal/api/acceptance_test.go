package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"oceanai/backend/internal/store"
)

func TestAcceptance(t *testing.T) {
	dsn := os.Getenv("ACCEPTANCE_DATABASE_URL")
	if dsn == "" {
		t.Skip("set ACCEPTANCE_DATABASE_URL to run PostgreSQL acceptance checks")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "acceptance_" + randomID()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = store.Bootstrap(ctx, pool); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.ReleaseMode)
	gin.DefaultWriter = io.Discard
	server := httptest.NewServer(Router(pool))
	defer server.Close()
	client := &http.Client{Timeout: 65 * time.Second}
	const alice = "alice@east.test"
	const bob = "bob@west.test"
	call := func(method, path, actor, key, body string) (int, []byte, error) {
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		if actor != "" {
			req.Header.Set("X-Actor-Email", actor)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		return resp.StatusCode, data, err
	}
	must := func(method, path, actor, key, body string, want int) []byte {
		t.Helper()
		status, data, err := call(method, path, actor, key, body)
		if err != nil || status != want {
			t.Fatalf("%s %s: status=%d want=%d err=%v body=%s", method, path, status, want, err, data)
		}
		return data
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var value int
		if err := pool.QueryRow(ctx, query, args...).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	sql := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	list := func(path, actor string) struct {
		Items []Certificate `json:"items"`
		Total int           `json:"total"`
	} {
		t.Helper()
		var result struct {
			Items []Certificate `json:"items"`
			Total int           `json:"total"`
		}
		if err := json.Unmarshal(must("GET", path, actor, "", "", 200), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	t.Run("A1_tenant_isolation", func(t *testing.T) {
		beforeCertificates := count("SELECT count(*) FROM certificates")
		beforeAudits := count("SELECT count(*) FROM audit_events")
		body := string(must("GET", "/api/courses", bob, "", "", 200))
		if strings.Contains(body, "east") || !strings.Contains(body, "west-safety") {
			t.Fatal(body)
		}
		result := list("/api/certificates?tenant_id=east", bob)
		if result.Total != 1 || result.Items[0].ID != "west-demo-certificate" {
			t.Fatal(result)
		}
		if result := list("/api/certificates?search=EAST", bob); result.Total != 0 {
			t.Fatal(result)
		}
		denied := must("GET", "/api/certificates/east-demo-certificate", bob, "", "", 404)
		missing := must("GET", "/api/certificates/missing", bob, "", "", 404)
		if !bytes.Equal(denied, missing) {
			t.Fatal("detail reveals cross-tenant existence")
		}
		must("POST", "/api/courses/east-safety/issue", bob, "cross-tenant", "{}", 404)
		must("POST", "/api/certificates/east-demo-certificate/revoke", bob, "", "", 404)
		csvBody := string(must("GET", "/api/certificates/export?tenant_id=east", bob, "", "", 200))
		if strings.Contains(csvBody, "EAST") || !strings.Contains(csvBody, "WEST-DEMO-0001") {
			t.Fatal(csvBody)
		}
		if count("SELECT count(*) FROM certificates") != beforeCertificates || count("SELECT count(*) FROM audit_events") != beforeAudits || count("SELECT count(*) FROM bulk_operations") != 0 {
			t.Fatal("rejected access changed data")
		}
		for _, actor := range []string{"", "unknown@example.test"} {
			must("GET", "/api/certificates", actor, "", "", 401)
		}
	})

	t.Run("A2_A4_same_key_concurrency_and_outcomes", func(t *testing.T) {
		var wg sync.WaitGroup
		results := make([]IssueResult, 12)
		failures := make(chan error, 12)
		start := make(chan struct{})
		for i := range results {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				status, body, err := call("POST", "/api/courses/east-safety/issue", alice, "shared-key", "{}")
				if err != nil || status != 200 {
					failures <- fmt.Errorf("status=%d err=%v body=%s", status, err, body)
					return
				}
				if err := json.Unmarshal(body, &results[i]); err != nil {
					failures <- err
				}
			}(i)
		}
		close(start)
		wg.Wait()
		close(failures)
		for err := range failures {
			t.Error(err)
		}
		want := map[string]int{"issued": 1, "skipped": 2, "failed": 0}
		if !reflect.DeepEqual(results[0].Summary, want) {
			t.Fatalf("outcomes: %+v", results[0])
		}
		for _, result := range results {
			if !reflect.DeepEqual(result, results[0]) {
				t.Fatal("concurrent responses differ")
			}
		}
		var replay IssueResult
		if err := json.Unmarshal(must("POST", "/api/courses/east-safety/issue", alice, "shared-key", " { } ", 200), &replay); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(replay, results[0]) {
			t.Fatal("sequential replay differs")
		}
		if count("SELECT count(*) FROM bulk_operations WHERE idempotency_key='shared-key'") != 1 || count("SELECT count(*) FROM certificates WHERE tenant_id='east'") != 2 || count("SELECT count(*) FROM audit_events WHERE tenant_id='east' AND action='ISSUED'") != 2 {
			t.Fatal("duplicate operation, certificates, or audits")
		}
		for _, outcome := range replay.Outcomes {
			switch outcome.LearnerID {
			case "east-alex":
				if outcome.Reason != "already_certified" {
					t.Fatal(outcome)
				}
			case "east-sam":
				if outcome.Reason != "incomplete" {
					t.Fatal(outcome)
				}
			case "east-jamie":
				if outcome.Outcome != "issued" {
					t.Fatal(outcome)
				}
			}
		}
	})

	t.Run("A3_key_misuse_preserves_result", func(t *testing.T) {
		original := must("POST", "/api/courses/east-safety/issue", alice, "shared-key", "{}", 200)
		certificates, audits := count("SELECT count(*) FROM certificates"), count("SELECT count(*) FROM audit_events")
		must("POST", "/api/courses/east-first-aid/issue", alice, "shared-key", "{}", 409)
		must("POST", "/api/courses/east-safety/issue", alice, "shared-key", `{"changed":true}`, 409)
		replay := must("POST", "/api/courses/east-safety/issue", alice, "shared-key", "{}", 200)
		if !bytes.Equal(original, replay) || count("SELECT count(*) FROM certificates") != certificates || count("SELECT count(*) FROM audit_events") != audits {
			t.Fatal("key misuse changed committed operation")
		}
		must("POST", "/api/courses/east-safety/issue", alice, "", "{}", 400)
		must("POST", "/api/courses/east-safety/issue", alice, "invalid-body", `{"changed":true}`, 400)
		if count("SELECT count(*) FROM bulk_operations WHERE idempotency_key='invalid-body'") != 0 {
			t.Fatal("invalid operation persisted")
		}
	})

	t.Run("A2_different_keys_compete", func(t *testing.T) {
		sql(`INSERT INTO enrollments VALUES ('east','east-first-aid','east-jamie',now())`)
		var wg sync.WaitGroup
		failures := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				status, body, err := call("POST", "/api/courses/east-first-aid/issue", alice, fmt.Sprintf("different-%d", i), "{}")
				if err != nil || status != 200 {
					failures <- fmt.Errorf("status=%d err=%v body=%s", status, err, body)
				}
			}(i)
		}
		wg.Wait()
		close(failures)
		for err := range failures {
			t.Error(err)
		}
		if count("SELECT count(*) FROM certificates WHERE course_id='east-first-aid'") != 1 || count("SELECT count(*) FROM audit_events a JOIN certificates c ON c.id=a.certificate_id WHERE c.course_id='east-first-aid'") != 1 {
			t.Fatal("competing requests duplicated certificates/audits")
		}
	})

	t.Run("learner_failure_rolls_back_certificate_and_audit", func(t *testing.T) {
		sql(`CREATE FUNCTION reject_test_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
            IF NEW.certificate_id IN (SELECT id FROM certificates WHERE learner_id='west-jamie') THEN
                RAISE EXCEPTION 'test-only constraint' USING ERRCODE='23514'; END IF; RETURN NEW; END $$;
            CREATE TRIGGER fail_test_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_test_audit()`)
		defer sql("DROP TRIGGER fail_test_audit ON audit_events; DROP FUNCTION reject_test_audit()")
		var result IssueResult
		json.Unmarshal(must("POST", "/api/courses/west-safety/issue", bob, "failure-key", "{}", 200), &result)
		if result.Summary["failed"] != 1 || result.Summary["issued"] != 0 {
			t.Fatal(result)
		}
		if count("SELECT count(*) FROM certificates WHERE learner_id='west-jamie'") != 0 {
			t.Fatal("failed audit left an unaudited certificate")
		}
	})

	t.Run("A5_dates_and_list_contract", func(t *testing.T) {
		for _, tc := range []struct{ actor, id, local, date string }{{alice, "east-demo-certificate", "2026-01-11 00:30", "2026-01-11"}, {bob, "west-demo-certificate", "2026-01-10 21:30", "2026-01-10"}} {
			var cert Certificate
			json.Unmarshal(must("GET", "/api/certificates/"+tc.id, tc.actor, "", "", 200), &cert)
			if cert.IssuedLocal != tc.local || cert.IssueDate != tc.date {
				t.Fatal(cert)
			}
			records, err := csv.NewReader(bytes.NewReader(must("GET", "/api/certificates/export?search=DEMO", tc.actor, "", "", 200))).ReadAll()
			if err != nil || len(records) != 2 || records[1][3] != tc.date {
				t.Fatalf("csv=%v err=%v", records, err)
			}
		}
		// Same local calendar day, opposite timestamp/ID ordering: date sorting must use the ID tie-breaker.
		sql(`UPDATE certificates SET issued_at='2026-01-10T14:30:00Z' WHERE course_id='east-first-aid';
            UPDATE certificates SET issued_at='2026-01-10T13:45:00Z' WHERE learner_id='east-jamie' AND course_id='east-safety'`)
		ordered := list("/api/certificates?sort=issue_date&direction=desc&page_size=100", alice)
		for i := 1; i < len(ordered.Items); i++ {
			if ordered.Items[i-1].IssueDate == ordered.Items[i].IssueDate && ordered.Items[i-1].ID > ordered.Items[i].ID {
				t.Fatal("date tie order is not ID ascending")
			}
		}
		first := list("/api/certificates?page_size=1&page=1", alice)
		second := list("/api/certificates?page_size=1&page=2", alice)
		if first.Total != 3 || len(first.Items) != 1 || first.Items[0].ID == second.Items[0].ID {
			t.Fatal("pagination failed")
		}
		if result := list("/api/certificates?page=999", alice); len(result.Items) != 0 || result.Total != 3 {
			t.Fatal(result)
		}
		if result := list("/api/certificates?search=not-present", alice); len(result.Items) != 0 || result.Total != 0 {
			t.Fatal(result)
		}
		for _, query := range []string{"page=0", "page=-1", "page=abc", "page_size=101", "page_size=0", "sort=bad", "direction=bad", "page=9223372036854775807"} {
			must("GET", "/api/certificates?"+query, alice, "", "", 400)
		}
		must("GET", "/api/certificates?search="+strings.Repeat("a", 201), alice, "", "", 400)
		sql(`UPDATE learners SET name=$1 WHERE id='east-alex'`, `Alex 100%_\ "Lee", Test`)
		literal := url.QueryEscape(`%_\`)
		if result := list("/api/certificates?search="+literal, alice); result.Total != 1 || result.Items[0].ID != "east-demo-certificate" {
			t.Fatal(result)
		}
		if result := list("/api/certificates?search="+url.QueryEscape(`' OR 1=1 --`), bob); result.Total != 0 {
			t.Fatal("search was not parameterized")
		}
		records, err := csv.NewReader(bytes.NewReader(must("GET", "/api/certificates/export?search=DEMO", alice, "", "", 200))).ReadAll()
		if err != nil || records[1][0] != `Alex 100%_\ "Lee", Test` {
			t.Fatalf("CSV quoting: %v %v", records, err)
		}
		sql(`UPDATE learners SET name='Alex Lee' WHERE id='east-alex'`)
	})

	t.Run("A7_revocation_is_repeat_safe", func(t *testing.T) {
		var wg sync.WaitGroup
		failures := make(chan error, 10)
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				status, body, err := call("POST", "/api/certificates/east-demo-certificate/revoke", alice, "", "")
				if err != nil || status != 200 || !bytes.Contains(body, []byte("REVOKED")) {
					failures <- fmt.Errorf("%d %s %v", status, body, err)
				}
			}()
		}
		wg.Wait()
		close(failures)
		for err := range failures {
			t.Error(err)
		}
		must("POST", "/api/certificates/east-demo-certificate/revoke", alice, "", "", 200)
		if count("SELECT count(*) FROM audit_events WHERE certificate_id='east-demo-certificate' AND action='REVOKED'") != 1 {
			t.Fatal("duplicate revoke audit")
		}
		var result IssueResult
		json.Unmarshal(must("POST", "/api/courses/east-safety/issue", alice, "after-revoke", "{}", 200), &result)
		if result.Summary["issued"] != 0 {
			t.Fatal("revoked certificate was reissued")
		}
	})

	t.Run("A6_100000_row_exports", func(t *testing.T) {
		if err := store.GenerateLarge(ctx, pool, "east", 100000); err != nil {
			t.Fatal(err)
		}
		if err := store.GenerateLarge(ctx, pool, "west", 100); err != nil {
			t.Fatal(err)
		}
		if err := store.GenerateLarge(ctx, pool, "east", 100000); err != nil {
			t.Fatal(err)
		}
		if count("SELECT count(*) FROM certificates WHERE tenant_id='east' AND course_id='east-load'") != 100000 || count("SELECT count(*) FROM audit_events WHERE tenant_id='east' AND certificate_id LIKE 'east-load-cert-%'") != 100000 {
			t.Fatal("large fixture is not repeatable")
		}
		// Independent oracle: exact matching values and ordering, including ties and local dates.
		expected := [][]string{{"learner_name", "course_title", "serial_number", "issue_date"}}
		rows, err := pool.Query(ctx, `SELECT l.name,co.title,c.serial_number,to_char(c.issued_at AT TIME ZONE 'Australia/Sydney','YYYY-MM-DD')
            FROM certificates c JOIN learners l ON l.id=c.learner_id JOIN courses co ON co.id=c.course_id
            WHERE c.tenant_id='east' AND l.name LIKE 'Load Learner %' ORDER BY l.name DESC,c.id ASC`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]string, 4)
			if err := rows.Scan(&values[0], &values[1], &values[2], &values[3]); err != nil {
				t.Fatal(err)
			}
			expected = append(expected, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		var first []byte
		for run := 1; run <= 2; run++ {
			started := time.Now()
			req, _ := http.NewRequest("GET", server.URL+"/api/certificates/export?search=Load%20Learner&sort=learner_name&direction=desc&page_size=1", nil)
			req.Header.Set("X-Actor-Email", alice)
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != 200 {
				t.Fatalf("export status %d", response.StatusCode)
			}
			listDone := make(chan time.Time, 1)
			listErrors := make(chan error, 1)
			if run == 1 {
				go func() {
					status, _, err := call("GET", "/api/certificates?page_size=1", alice, "", "")
					if err != nil || status != 200 {
						listErrors <- fmt.Errorf("list during export: status=%d err=%v", status, err)
					}
					listDone <- time.Now()
				}()
			}
			var reader io.Reader = response.Body
			// A realistic slower client ensures overlap even when the local export is very fast.
			// Only the first download is paced; the second measures unrestricted throughput.
			if run == 1 {
				reader = pacedReader{response.Body}
			}
			data, err := io.ReadAll(reader)
			response.Body.Close()
			finished := time.Now()
			if err != nil {
				t.Fatal(err)
			}
			if run == 1 {
				when := <-listDone
				select {
				case err := <-listErrors:
					t.Fatal(err)
				default:
				}
				if !when.Before(finished) {
					t.Fatal("list did not complete while export was running")
				}
				t.Logf("A6 concurrent list completed after %s", when.Sub(started))
			}
			if finished.Sub(started) >= 60*time.Second {
				t.Fatal("export exceeded 60 seconds")
			}
			records, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
			if err != nil || !reflect.DeepEqual(records, expected) {
				t.Fatalf("export differs from exact 100000-row oracle: rows=%d err=%v", len(records)-1, err)
			}
			if run == 1 {
				first = data
			} else if !bytes.Equal(first, data) {
				t.Fatal("repeated export order changed")
			}
			t.Logf("A6 export %d: rows=%d bytes=%d elapsed=%s", run, len(records)-1, len(data), finished.Sub(started))
		}
		if result := list("/api/certificates?search=Load%20Learner%20007&page_size=1", alice); result.Total != 1000 {
			t.Fatalf("selective search total %d", result.Total)
		}
	})
}

type pacedReader struct{ io.Reader }

func (r pacedReader) Read(p []byte) (int, error) {
	if len(p) > 32768 {
		p = p[:32768]
	}
	n, err := r.Reader.Read(p)
	if n > 0 {
		time.Sleep(10 * time.Millisecond)
	}
	return n, err
}
