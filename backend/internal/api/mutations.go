package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Outcome struct {
	LearnerID     string `json:"learner_id"`
	Outcome       string `json:"outcome"`
	Reason        string `json:"reason,omitempty"`
	CertificateID string `json:"certificate_id,omitempty"`
}
type IssueResult struct {
	OperationID string         `json:"operation_id"`
	Status      string         `json:"status"`
	Summary     map[string]int `json:"summary"`
	Outcomes    []Outcome      `json:"outcomes"`
}

func randomID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value[:])
}

func canonicalBody(c *gin.Context) ([]byte, error) {
	reader := http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	defer reader.Close()
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	var body map[string]any
	err := decoder.Decode(&body)
	if err == io.EOF {
		return []byte("{}"), nil
	}
	if err != nil || body == nil {
		return nil, fmt.Errorf("body must be a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("body must contain one JSON object")
	}
	return json.Marshal(body)
}

func issueHandler(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		a := c.MustGet("actor").(Actor)
		course := c.Param("id")
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM courses WHERE tenant_id=$1 AND id=$2)`, a.TenantID, course).Scan(&exists); err != nil {
			internalError(c, err)
			return
		}
		if !exists {
			c.JSON(404, gin.H{"error": "course not found"})
			return
		}
		key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if key == "" || len(key) > 200 {
			c.JSON(400, gin.H{"error": "Idempotency-Key is required (maximum 200 bytes)"})
			return
		}
		body, err := canonicalBody(c)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		fingerprint, _ := json.Marshal([]string{"POST /api/courses/:id/issue", course, string(body)})
		hash := fmt.Sprintf("%x", sha256.Sum256(fingerprint))
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback(context.Background())
		operationID := randomID()
		var claimed string
		err = tx.QueryRow(ctx, `INSERT INTO bulk_operations (id, tenant_id, actor_email, idempotency_key, request_hash)
            VALUES ($1,$2,$3,$4,$5) ON CONFLICT (tenant_id, actor_email, idempotency_key) DO NOTHING RETURNING id`,
			operationID, a.TenantID, a.Email, key, hash).Scan(&claimed)
		if errors.Is(err, pgx.ErrNoRows) {
			var storedHash string
			var status int
			var response []byte
			err = tx.QueryRow(ctx, `SELECT request_hash, response_status, response_body FROM bulk_operations
                WHERE tenant_id=$1 AND actor_email=$2 AND idempotency_key=$3`, a.TenantID, a.Email, key).Scan(&storedHash, &status, &response)
			if err != nil {
				internalError(c, err)
				return
			}
			if storedHash != hash {
				c.JSON(409, gin.H{"error": "idempotency key was used for a different request"})
				return
			}
			if err = tx.Commit(ctx); err != nil {
				internalError(c, err)
				return
			}
			c.Data(status, "application/json", response)
			return
		}
		if err != nil {
			internalError(c, err)
			return
		}
		// Compare an existing key before rejecting changed fields, so key misuse is always visible.
		if !bytes.Equal(body, []byte("{}")) {
			c.JSON(400, gin.H{"error": "issuance body must be empty: {}"})
			return
		}
		rows, err := tx.Query(ctx, `SELECT learner_id, completed_at IS NOT NULL FROM enrollments
            WHERE tenant_id=$1 AND course_id=$2 ORDER BY learner_id`, a.TenantID, course)
		if err != nil {
			internalError(c, err)
			return
		}
		type enrollment struct {
			id       string
			complete bool
		}
		enrollments := []enrollment{}
		for rows.Next() {
			var e enrollment
			if err := rows.Scan(&e.id, &e.complete); err != nil {
				rows.Close()
				internalError(c, err)
				return
			}
			enrollments = append(enrollments, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			internalError(c, err)
			return
		}
		result := IssueResult{OperationID: operationID, Status: "completed", Summary: map[string]int{"issued": 0, "skipped": 0, "failed": 0}, Outcomes: []Outcome{}}
		for _, e := range enrollments {
			outcome := Outcome{LearnerID: e.id, Outcome: "skipped", Reason: "incomplete"}
			if e.complete {
				if _, err = tx.Exec(ctx, "SAVEPOINT learner"); err != nil {
					internalError(c, err)
					return
				}
				certificateID := randomID()
				var inserted string
				err = tx.QueryRow(ctx, `INSERT INTO certificates (id, tenant_id, course_id, learner_id, serial_number, issued_at)
                    VALUES ($1,$2,$3,$4,$5,now()) ON CONFLICT (tenant_id, course_id, learner_id) DO NOTHING RETURNING id`,
					certificateID, a.TenantID, course, e.id, strings.ToUpper(a.TenantID)+"-"+certificateID).Scan(&inserted)
				if errors.Is(err, pgx.ErrNoRows) {
					outcome.Reason = "already_certified"
					err = nil
				} else if err == nil {
					_, err = tx.Exec(ctx, `INSERT INTO audit_events (tenant_id, certificate_id, actor_email, action, occurred_at)
                        VALUES ($1,$2,$3,'ISSUED',now())`, a.TenantID, certificateID, a.Email)
					outcome = Outcome{LearnerID: e.id, Outcome: "issued", CertificateID: certificateID}
				}
				if err != nil {
					var pgErr *pgconn.PgError
					// Only row-level constraint failures are recoverable; connection/deadlock/timeouts abort everything.
					if !errors.As(err, &pgErr) || !strings.HasPrefix(pgErr.Code, "23") {
						internalError(c, err)
						return
					}
					if _, rollbackErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT learner"); rollbackErr != nil {
						internalError(c, rollbackErr)
						return
					}
					outcome = Outcome{LearnerID: e.id, Outcome: "failed", Reason: "constraint_violation"}
				}
				if _, err = tx.Exec(ctx, "RELEASE SAVEPOINT learner"); err != nil {
					internalError(c, err)
					return
				}
			}
			result.Outcomes = append(result.Outcomes, outcome)
			result.Summary[outcome.Outcome]++
		}
		response, err := json.Marshal(result)
		if err != nil {
			internalError(c, err)
			return
		}
		if _, err = tx.Exec(ctx, `UPDATE bulk_operations SET response_status=200, response_body=$2::jsonb WHERE id=$1`, operationID, string(response)); err != nil {
			internalError(c, err)
			return
		}
		if err = tx.Commit(ctx); err != nil {
			internalError(c, err)
			return
		}
		c.Data(200, "application/json", response)
	}
}

func revokeHandler(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		a := c.MustGet("actor").(Actor)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		tx, err := pool.Begin(ctx)
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback(context.Background())
		var id string
		err = tx.QueryRow(ctx, `UPDATE certificates SET status='REVOKED'
            WHERE tenant_id=$1 AND id=$2 AND status='ACTIVE' RETURNING id`, a.TenantID, c.Param("id")).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			internalError(c, err)
			return
		}
		if err == nil {
			if _, err = tx.Exec(ctx, `INSERT INTO audit_events (tenant_id, certificate_id, actor_email, action, occurred_at)
                VALUES ($1,$2,$3,'REVOKED',now())`, a.TenantID, id, a.Email); err != nil {
				internalError(c, err)
				return
			}
		}
		cert, err := scanCertificate(tx.QueryRow(ctx, certificateSelect+` WHERE c.tenant_id=$1 AND c.id=$2`, a.TenantID, c.Param("id")), a.Timezone)
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(404, gin.H{"error": "certificate not found"})
			return
		}
		if err != nil {
			internalError(c, err)
			return
		}
		if err = tx.Commit(ctx); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(200, cert)
	}
}
