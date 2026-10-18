-- Migration 16: runtime-selected children for agent walkthroughs (ADR-0063).
-- Applied migrations are history: never edit them; add a new one.

ALTER TABLE walkthrough_events
    ADD COLUMN allow_dynamic_children INTEGER NOT NULL DEFAULT 0
        CHECK (allow_dynamic_children IN (0, 1));

ALTER TABLE walkthrough_events
    ADD COLUMN dynamic_child_key TEXT;

CREATE UNIQUE INDEX walkthrough_dynamic_child_per_token
    ON walkthrough_events (token, dynamic_child_key)
    WHERE dynamic_child_key IS NOT NULL;
