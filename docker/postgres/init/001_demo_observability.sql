CREATE TABLE service_requests (
    recorded_at timestamptz NOT NULL,
    service text NOT NULL,
    endpoint text NOT NULL,
    status_code integer NOT NULL,
    duration_ms double precision NOT NULL,
    request_count integer NOT NULL,
    CONSTRAINT service_requests_status_code_check CHECK (status_code BETWEEN 100 AND 599),
    CONSTRAINT service_requests_duration_check CHECK (duration_ms >= 0),
    CONSTRAINT service_requests_count_check CHECK (request_count > 0)
);

CREATE INDEX service_requests_recorded_at_idx ON service_requests (recorded_at);
CREATE INDEX service_requests_service_recorded_at_idx ON service_requests (service, recorded_at);

INSERT INTO service_requests (
    recorded_at,
    service,
    endpoint,
    status_code,
    duration_ms,
    request_count
)
SELECT
    minute,
    service,
    endpoint,
    CASE
        WHEN sample % 29 = 0 THEN 500
        WHEN sample % 17 = 0 THEN 404
        ELSE 200
    END,
    round((35 + (sample % 90) + random() * 80)::numeric, 2)::double precision,
    20 + (sample % 45)
FROM generate_series(
    date_trunc('minute', now()) - interval '24 hours',
    date_trunc('minute', now()),
    interval '1 minute'
) WITH ORDINALITY AS series(minute, sample)
CROSS JOIN (VALUES
    ('checkout-api', '/api/checkout'),
    ('catalog-api', '/api/products'),
    ('identity-api', '/api/login')
) AS dimensions(service, endpoint);

COMMENT ON TABLE service_requests IS
    'Synthetic one-minute service observations for validating AI-generated Grafana dashboards.';
