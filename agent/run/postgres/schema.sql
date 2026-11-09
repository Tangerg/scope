CREATE TABLE $schema.log (
    root text NOT NULL,
    lsn numeric(20,0) NOT NULL CHECK (lsn BETWEEN 1 AND 18446744073709551615),
    epoch numeric(20,0) NOT NULL CHECK (epoch BETWEEN 0 AND 18446744073709551615),
    kind text NOT NULL,
    body bytea NOT NULL,
    PRIMARY KEY (root, lsn)
);
-- Load starts at the newest checkpoint; this index finds it without reading
-- the history before it.
CREATE INDEX log_checkpoints ON $schema.log (root, lsn) WHERE kind = $checkpoint;
