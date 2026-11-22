ALTER TABLE admins ADD CONSTRAINT admins_tenant_email_key UNIQUE (tenant_id, email);
ALTER TABLE certificates ADD CONSTRAINT certificates_tenant_id_key UNIQUE (tenant_id, id);
ALTER TABLE audit_events ADD COLUMN tenant_id text;
UPDATE audit_events a SET tenant_id = c.tenant_id FROM certificates c WHERE c.id = a.certificate_id;
ALTER TABLE audit_events ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE audit_events ADD CONSTRAINT audit_certificate_tenant_fk
    FOREIGN KEY (tenant_id, certificate_id) REFERENCES certificates (tenant_id, id);
ALTER TABLE audit_events ADD CONSTRAINT audit_actor_tenant_fk
    FOREIGN KEY (tenant_id, actor_email) REFERENCES admins (tenant_id, email);

CREATE TABLE bulk_operations (
    id text PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES tenants(id),
    actor_email text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    response_status integer,
    response_body jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, actor_email, idempotency_key),
    FOREIGN KEY (tenant_id, actor_email) REFERENCES admins (tenant_id, email)
);

INSERT INTO courses (id, tenant_id, title) VALUES ('east-first-aid', 'east', 'First Aid');
