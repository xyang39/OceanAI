package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Actor struct {
	Email      string `json:"email"`
	TenantID   string `json:"tenant_id"`
	TenantName string `json:"tenant_name"`
	Timezone   string `json:"timezone"`
}

type Certificate struct {
	ID           string    `json:"id"`
	SerialNumber string    `json:"serial_number"`
	LearnerName  string    `json:"learner_name"`
	CourseTitle  string    `json:"course_title"`
	IssuedAt     time.Time `json:"issued_at"`
	IssuedLocal  string    `json:"issued_local"`
	IssueDate    string    `json:"issue_date"`
	Status       string    `json:"status"`
}

const certificateSelect = `SELECT c.id, c.serial_number, l.name, co.title, c.issued_at, c.status
	FROM certificates c
	JOIN learners l ON l.tenant_id = c.tenant_id AND l.id = c.learner_id
	JOIN courses co ON co.tenant_id = c.tenant_id AND co.id = c.course_id`

func Router(pool *pgxpool.Pool) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.CustomRecovery(func(c *gin.Context, recovered any) {
		if recovered == http.ErrAbortHandler {
			panic(recovered)
		}
		c.AbortWithStatusJSON(500, gin.H{"error": "internal server error"})
	}))
	_ = r.SetTrustedProxies(nil)
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/api/health", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "database": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "database": "connected", "service": "oceanai-gin"})
	})
	// Public only for the assessment's explicit demo actor selector; not authentication.
	r.GET("/api/actors", func(c *gin.Context) {
		rows, err := pool.Query(c.Request.Context(), `SELECT a.email, t.id, t.name, t.timezone
			FROM admins a JOIN tenants t ON t.id = a.tenant_id ORDER BY a.email`)
		if err != nil {
			internalError(c, err)
			return
		}
		defer rows.Close()
		actors := []Actor{}
		for rows.Next() {
			var a Actor
			if err := rows.Scan(&a.Email, &a.TenantID, &a.TenantName, &a.Timezone); err != nil {
				internalError(c, err)
				return
			}
			actors = append(actors, a)
		}
		if err := rows.Err(); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": actors})
	})
	api := r.Group("/api", actorContext(pool))
	api.GET("/courses", coursesHandler(pool))
	api.GET("/certificates", listHandler(pool))
	api.GET("/certificates/export", exportHandler(pool))
	api.POST("/courses/:id/issue", issueHandler(pool))
	api.POST("/certificates/:id/revoke", revokeHandler(pool))
	api.GET("/certificates/:id", func(c *gin.Context) {
		a := c.MustGet("actor").(Actor)
		row := pool.QueryRow(c.Request.Context(), certificateSelect+` WHERE c.tenant_id = $1 AND c.id = $2`, a.TenantID, c.Param("id"))
		cert, err := scanCertificate(row, a.Timezone)
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "certificate not found"})
			return
		}
		if err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, cert)
	})
	r.NoRoute(func(c *gin.Context) { c.JSON(http.StatusNotFound, gin.H{"error": "route not found"}) })
	return r
}

func actorContext(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		email := c.GetHeader("X-Actor-Email")
		if email == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "X-Actor-Email header is required"})
			return
		}
		var a Actor
		err := pool.QueryRow(c.Request.Context(), `SELECT a.email, t.id, t.name, t.timezone
			FROM admins a JOIN tenants t ON t.id = a.tenant_id WHERE a.email = $1`, email).
			Scan(&a.Email, &a.TenantID, &a.TenantName, &a.Timezone)
		if errors.Is(err, pgx.ErrNoRows) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unknown demo actor"})
			return
		}
		if err != nil {
			internalError(c, err)
			c.Abort()
			return
		}
		c.Set("actor", a)
		c.Next()
	}
}

func scanCertificate(row pgx.Row, timezone string) (Certificate, error) {
	var cert Certificate
	if err := row.Scan(&cert.ID, &cert.SerialNumber, &cert.LearnerName, &cert.CourseTitle, &cert.IssuedAt, &cert.Status); err != nil {
		return cert, err
	}
	local, date, err := localIssueDate(cert.IssuedAt, timezone)
	cert.IssuedLocal, cert.IssueDate = local, date
	return cert, err
}

func localIssueDate(issued time.Time, timezone string) (string, string, error) {
	zone, err := time.LoadLocation(timezone)
	if err != nil {
		return "", "", err
	}
	local := issued.In(zone)
	return local.Format("2006-01-02 15:04"), local.Format("2006-01-02"), nil
}

func internalError(c *gin.Context, err error) {
	log.Printf("request failed: %v", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
}
