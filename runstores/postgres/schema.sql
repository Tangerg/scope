CREATE TABLE $schema.tree_heads (
    root_id text PRIMARY KEY,
    snapshot bytea NOT NULL,
    sequence numeric(20,0) NOT NULL CHECK (sequence BETWEEN 0 AND 18446744073709551615)
);
CREATE TABLE $schema.commit_facts (
    identity text PRIMARY KEY,
    scope_content text NOT NULL,
    business_content text NOT NULL
);
CREATE TABLE $schema.start_receipts (
    scope bytea NOT NULL,
    start_key bytea NOT NULL,
    intent text NOT NULL,
    root_id text NOT NULL UNIQUE REFERENCES $schema.tree_heads(root_id) DEFERRABLE INITIALLY DEFERRED,
    PRIMARY KEY (scope, start_key)
);
