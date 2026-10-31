
CREATE TABLE api_requests (
 request_id TEXT PRIMARY KEY,
 kind TEXT NOT NULL CHECK(kind IN ('start','finish')),
 at TEXT NOT NULL,
 started_at TEXT NOT NULL,
 provider_id TEXT NOT NULL,
 provider TEXT NOT NULL,
 profile TEXT NOT NULL,
 model TEXT NOT NULL,
 operation TEXT NOT NULL,
 status TEXT NOT NULL,
 input_tokens INTEGER CHECK(input_tokens >= 0),
 output_tokens INTEGER CHECK(output_tokens >= 0),
 total_tokens INTEGER CHECK(total_tokens >= 0),
 run_id TEXT NOT NULL,
 pass_id TEXT NOT NULL,
 request_url TEXT NOT NULL DEFAULT '',
 request_payload BLOB,
 response_payload BLOB,
 http_status INTEGER NOT NULL DEFAULT 0 CHECK(http_status=0 OR http_status BETWEEN 100 AND 599),
 response_complete INTEGER CHECK(response_complete IN (0,1))
);
CREATE INDEX api_requests_started ON api_requests(started_at);
CREATE INDEX api_requests_provider_started ON api_requests(provider_id, started_at);
CREATE INDEX api_requests_review ON api_requests(run_id, pass_id);
CREATE TABLE review_runs (
 run_id TEXT NOT NULL,
 pass_id TEXT NOT NULL,
 plan_hash TEXT NOT NULL,
 started_at TEXT NOT NULL,
 finished_at TEXT,
 status TEXT NOT NULL CHECK(status IN ('unfinished','complete','partial','failed','canceled')),
 checked_blocks INTEGER NOT NULL CHECK(checked_blocks >= 0),
 warning_findings INTEGER NOT NULL CHECK(warning_findings >= 0),
 error_findings INTEGER NOT NULL CHECK(error_findings >= 0),
 critical_findings INTEGER NOT NULL CHECK(critical_findings >= 0),
 PRIMARY KEY(run_id, pass_id)
);
CREATE INDEX review_runs_started ON review_runs(started_at);
CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
PRAGMA application_id=1296323158;
PRAGMA user_version=2;
