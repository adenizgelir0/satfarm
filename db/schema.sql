CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    balance_satc BIGINT NOT NULL DEFAULT 0 CHECK (balance_satc >= 0),
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS api_tokens (
    id BIGSERIAL PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    label TEXT NOT NULL,
    secret TEXT NOT NULL UNIQUE, -- the actual token string
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    revoked BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS cnf_files (
    id BIGSERIAL PRIMARY KEY,

    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    original_name TEXT NOT NULL,         -- original uploaded filename
    storage_path  TEXT NOT NULL,         -- where it lives on disk
    size_bytes    BIGINT NOT NULL CHECK (size_bytes >= 0),

    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS drat_files (
    id BIGSERIAL PRIMARY KEY,

    user_id INTEGER NOT NULL
        REFERENCES users(id) ON DELETE CASCADE,

    -- original file name as uploaded or generated
    original_name TEXT NOT NULL,

    -- path inside container/filesystem where the DRAT file is stored
    storage_path TEXT NOT NULL,

    size_bytes BIGINT NOT NULL
        CHECK (size_bytes >= 0),

    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS jobs (
    id BIGSERIAL PRIMARY KEY,

    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    cnf_file_id BIGINT NOT NULL
        REFERENCES cnf_files(id) ON DELETE RESTRICT,

    name TEXT,                           -- optional display name

    max_workers INTEGER NOT NULL CHECK (max_workers > 0),

    status TEXT NOT NULL CHECK (
        status IN ('pending','running','completed','failed')
    ),

    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS job_shards (
    id BIGSERIAL PRIMARY KEY,

    job_id BIGINT NOT NULL
        REFERENCES jobs(id) ON DELETE CASCADE,

    -- the cube literals assigned to this shard, e.g. {1,-3,10}
    cube_literals INTEGER[] NOT NULL,

    status TEXT NOT NULL CHECK (
        status IN ('pending','running','completed','failed')
    ),

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS transactions (
    id BIGSERIAL PRIMARY KEY,

    user_id INTEGER NOT NULL
        REFERENCES users(id) ON DELETE CASCADE,

    -- positive = credit, negative = debit
    amount_satc BIGINT NOT NULL,

    -- short machine-readable reason, e.g. 'deposit', 'withdraw', 'job_charge'
    reason TEXT NOT NULL,

    -- optional free-form details (job id, shard id, etc.) for later
    meta JSONB,

    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS shard_results (
    id BIGSERIAL PRIMARY KEY,

    shard_id BIGINT NOT NULL
        REFERENCES job_shards(id) ON DELETE CASCADE,

    -- true  = SAT
    -- false = UNSAT
    is_sat BOOLEAN NOT NULL,

    -- For SAT shards: satisfying assignment as list of literals (e.g. {1, -3, 10, ...})
    -- For UNSAT shards: should be NULL
    assignment INTEGER[],

    -- For UNSAT shards: DRAT proof file
    -- For SAT shards: should be NULL
    drat_file_id BIGINT
        REFERENCES drat_files(id) ON DELETE SET NULL,

    -- Result verification status: 'unverified' or 'verified'
    status TEXT NOT NULL CHECK (
        status IN ('unverified', 'verified')
    ),

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),

    -- Enforce SAT/UNSAT invariants
    CHECK (
        (is_sat = TRUE  AND assignment IS NOT NULL AND drat_file_id IS NULL)
        OR
        (is_sat = FALSE AND assignment IS NULL     AND drat_file_id IS NOT NULL)
    ),

    -- REQUIRED for ON CONFLICT (shard_id)
    UNIQUE (shard_id)
);

CREATE TABLE IF NOT EXISTS job_results (
    id BIGSERIAL PRIMARY KEY,

    job_id BIGINT NOT NULL
        REFERENCES jobs(id) ON DELETE CASCADE,

    -- true  = SAT
    -- false = UNSAT
    is_sat BOOLEAN NOT NULL,

    -- For SAT jobs: satisfying assignment as list of literals (e.g. {1, -3, 10, ...})
    -- For UNSAT jobs: must be NULL
    assignment INTEGER[],

    created_at TIMESTAMP NOT NULL DEFAULT NOW(),

    -- Enforce that SAT has assignment, UNSAT has none
    CHECK (
        (is_sat = TRUE  AND assignment IS NOT NULL) OR
        (is_sat = FALSE AND assignment IS NULL)
    ),

    -- There should be at most one result per job
    UNIQUE (job_id)
);