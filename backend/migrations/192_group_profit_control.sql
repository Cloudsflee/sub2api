-- Per-group profit control for scheduling admission.
-- Admission rule at request time: an account qualifies iff its cost multiplier
-- U (accounts.rate_multiplier) satisfies U <= D * (1 - margin - buffer), where
-- D is the requester's effective downstream multiplier at the request's
-- pricing instant.
ALTER TABLE groups
    -- Keep this migration additive for the production migration gate.  The
    -- defaults backfill existing rows while the generated Ent schema still
    -- enforces the fields for all new writes.
    ADD COLUMN IF NOT EXISTS profit_control_enabled BOOLEAN DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS profit_min_margin DECIMAL(10,4) DEFAULT 0,
    ADD COLUMN IF NOT EXISTS profit_safety_buffer DECIMAL(10,4) DEFAULT 0;
