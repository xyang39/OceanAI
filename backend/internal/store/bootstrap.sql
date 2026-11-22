-- Version 1: framework demo. Executed once inside a transaction by Bootstrap.
CREATE TABLE tenants (
    id text PRIMARY KEY,
    name text NOT NULL,
    timezone text NOT NULL
);

CREATE TABLE admins (
    email text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES tenants(id)
);

CREATE TABLE courses (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES tenants(id),
    title text NOT NULL,
    UNIQUE (tenant_id, id)
);

CREATE TABLE learners (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES tenants(id),
    name text NOT NULL,
    UNIQUE (tenant_id, id)
);

CREATE TABLE enrollments (
    tenant_id text NOT NULL REFERENCES tenants(id),
    course_id text NOT NULL,
    learner_id text NOT NULL,
    completed_at timestamptz,
    PRIMARY KEY (tenant_id, course_id, learner_id),
    FOREIGN KEY (tenant_id, course_id) REFERENCES courses(tenant_id, id),
    FOREIGN KEY (tenant_id, learner_id) REFERENCES learners(tenant_id, id)
);

CREATE TABLE certificates (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES tenants(id),
    course_id text NOT NULL,
    learner_id text NOT NULL,
    serial_number text NOT NULL UNIQUE,
    issued_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'REVOKED')),
    UNIQUE (tenant_id, course_id, learner_id),
    FOREIGN KEY (tenant_id, course_id, learner_id)
        REFERENCES enrollments(tenant_id, course_id, learner_id)
);

CREATE TABLE audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    certificate_id text NOT NULL REFERENCES certificates(id),
    actor_email text NOT NULL REFERENCES admins(email),
    action text NOT NULL CHECK (action IN ('ISSUED', 'REVOKED')),
    occurred_at timestamptz NOT NULL,
    UNIQUE (certificate_id, action)
);

CREATE INDEX certificates_tenant_issued ON certificates (tenant_id, issued_at, id);

INSERT INTO tenants VALUES ('east', 'East', 'Australia/Sydney'), ('west', 'West', 'Australia/Perth');
INSERT INTO admins VALUES ('alice@east.test', 'east'), ('bob@west.test', 'west');
INSERT INTO courses VALUES ('east-safety', 'east', 'Safety 101'), ('west-safety', 'west', 'Safety 101');
INSERT INTO learners VALUES
    ('east-alex', 'east', 'Alex Lee'), ('east-jamie', 'east', 'Jamie Chen'), ('east-sam', 'east', 'Sam Taylor'),
    ('west-alex', 'west', 'Alex Lee'), ('west-jamie', 'west', 'Jamie Chen'), ('west-sam', 'west', 'Sam Taylor');
INSERT INTO enrollments VALUES
    ('east', 'east-safety', 'east-alex', '2026-01-09T10:00:00Z'),
    ('east', 'east-safety', 'east-jamie', '2026-01-09T10:00:00Z'),
    ('east', 'east-safety', 'east-sam', NULL),
    ('west', 'west-safety', 'west-alex', '2026-01-09T10:00:00Z'),
    ('west', 'west-safety', 'west-jamie', '2026-01-09T10:00:00Z'),
    ('west', 'west-safety', 'west-sam', NULL);
INSERT INTO certificates (id, tenant_id, course_id, learner_id, serial_number, issued_at) VALUES
    ('east-demo-certificate', 'east', 'east-safety', 'east-alex', 'EAST-DEMO-0001', '2026-01-10T13:30:00Z'),
    ('west-demo-certificate', 'west', 'west-safety', 'west-alex', 'WEST-DEMO-0001', '2026-01-10T13:30:00Z');
INSERT INTO audit_events (certificate_id, actor_email, action, occurred_at) VALUES
    ('east-demo-certificate', 'alice@east.test', 'ISSUED', '2026-01-10T13:30:00Z'),
    ('west-demo-certificate', 'bob@west.test', 'ISSUED', '2026-01-10T13:30:00Z');
