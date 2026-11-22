package api

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type listOptions struct {
	Search, Sort, Direction string
	Page, PageSize          int64
}

func parseListOptions(q url.Values, paginate bool) (listOptions, error) {
	o := listOptions{Search: strings.TrimSpace(q.Get("search")), Sort: "issue_date", Direction: "desc", Page: 1, PageSize: 25}
	if utf8.RuneCountInString(o.Search) > 200 || strings.ContainsRune(o.Search, 0) {
		return o, fmt.Errorf("search must contain at most 200 characters and no NUL")
	}
	if q.Has("sort") {
		o.Sort = q.Get("sort")
	}
	if q.Has("direction") {
		o.Direction = q.Get("direction")
	}
	switch o.Sort {
	case "learner_name", "serial_number", "issue_date":
	default:
		return o, fmt.Errorf("unsupported sort")
	}
	if o.Direction != "asc" && o.Direction != "desc" {
		return o, fmt.Errorf("direction must be asc or desc")
	}
	if paginate {
		for key, target := range map[string]*int64{"page": &o.Page, "page_size": &o.PageSize} {
			if !q.Has(key) {
				continue
			}
			n, err := strconv.ParseInt(q.Get(key), 10, 64)
			if err != nil || n < 1 {
				return o, fmt.Errorf("%s must be a positive integer", key)
			}
			*target = n
		}
		if o.PageSize > 100 {
			return o, fmt.Errorf("page_size must be at most 100")
		}
		if o.Page-1 > math.MaxInt64/o.PageSize {
			return o, fmt.Errorf("page is too large")
		}
	}
	return o, nil
}

const certificateFrom = ` FROM certificates c
    JOIN learners l ON l.tenant_id=c.tenant_id AND l.id=c.learner_id
    JOIN courses co ON co.tenant_id=c.tenant_id AND co.id=c.course_id`

// Shared by list and export: user-controlled values are always bound parameters.
func certificateQuery(a Actor, o listOptions) (string, string, []any) {
	args := []any{a.TenantID, a.Timezone}
	// Reference the timezone in the shared query so both count and data queries use identical arguments.
	from := certificateFrom + ` CROSS JOIN (SELECT $2::text AS timezone) tz WHERE c.tenant_id=$1`
	if o.Search != "" {
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(o.Search)
		args = append(args, "%"+escaped+"%")
		from += ` AND (l.name ILIKE $3 ESCAPE '\' OR c.serial_number ILIKE $3 ESCAPE '\')`
	}
	column := map[string]string{"learner_name": "l.name", "serial_number": "c.serial_number", "issue_date": "(c.issued_at AT TIME ZONE tz.timezone)::date"}[o.Sort]
	order := " ORDER BY " + column + " " + strings.ToUpper(o.Direction) + ", c.id ASC"
	return from, order, args
}

func listHandler(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		o, err := parseListOptions(c.Request.URL.Query(), true)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		a := c.MustGet("actor").(Actor)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			internalError(c, err)
			return
		}
		defer tx.Rollback(context.Background())
		from, order, args := certificateQuery(a, o)
		var total int64
		if err = tx.QueryRow(ctx, "SELECT count(*)"+from, args...).Scan(&total); err != nil {
			internalError(c, err)
			return
		}
		query := "SELECT c.id, c.serial_number, l.name, co.title, c.issued_at, c.status" + from + order + fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
		args = append(args, o.PageSize, (o.Page-1)*o.PageSize)
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			internalError(c, err)
			return
		}
		items := []Certificate{}
		for rows.Next() {
			cert, scanErr := scanCertificate(rows, a.Timezone)
			if scanErr != nil {
				rows.Close()
				internalError(c, scanErr)
				return
			}
			items = append(items, cert)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			internalError(c, err)
			return
		}
		if err = tx.Commit(ctx); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(200, gin.H{"actor": a, "items": items, "total": total, "page": o.Page, "page_size": o.PageSize})
	}
}

func coursesHandler(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		a := c.MustGet("actor").(Actor)
		rows, err := pool.Query(c.Request.Context(), `SELECT id, title FROM courses WHERE tenant_id=$1 ORDER BY title, id`, a.TenantID)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()
		items := []gin.H{}
		for rows.Next() {
			var id, title string
			if err := rows.Scan(&id, &title); err != nil {
				internalError(c, err)
				return
			}
			items = append(items, gin.H{"id": id, "title": title})
		}
		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(200, gin.H{"items": items})
	}
}

func exportHandler(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		o, err := parseListOptions(c.Request.URL.Query(), false)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		a := c.MustGet("actor").(Actor)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
		defer cancel()
		from, order, args := certificateQuery(a, o)
		rows, err := pool.Query(ctx, `SELECT l.name, co.title, c.serial_number,
            to_char(c.issued_at AT TIME ZONE tz.timezone, 'YYYY-MM-DD')`+from+order, args...)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", `attachment; filename="certificates.csv"`)
		c.Header("X-Accel-Buffering", "no")
		c.Header("Cache-Control", "no-store")
		writer := csv.NewWriter(c.Writer)
		abort := func(err error) { log.Printf("export interrupted: %v", err); panic(http.ErrAbortHandler) }
		if err := writer.Write([]string{"learner_name", "course_title", "serial_number", "issue_date"}); err != nil {
			abort(err)
		}
		writer.Flush()
		c.Writer.Flush()
		count := 0
		for rows.Next() {
			var name, course, serial, date string
			if err := rows.Scan(&name, &course, &serial, &date); err != nil {
				abort(err)
			}
			if err := writer.Write([]string{name, course, serial, date}); err != nil {
				abort(err)
			}
			count++
			if count%256 == 0 {
				writer.Flush()
				if err := writer.Error(); err != nil {
					abort(err)
				}
				c.Writer.Flush()
			}
		}
		if err := rows.Err(); err != nil {
			abort(err)
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			abort(err)
		}
	}
}
