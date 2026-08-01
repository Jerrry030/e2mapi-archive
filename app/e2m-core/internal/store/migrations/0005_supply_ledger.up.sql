-- W5: supply ledger — allocations of supplier offers to managed instances.
-- Refs only; credentials move via the gateway admin API, never through here.
CREATE TABLE IF NOT EXISTS supply_ledger (
    id               TEXT PRIMARY KEY,
    offer_id         TEXT NOT NULL,
    supplier_user_id BIGINT NOT NULL,
    user_id          BIGINT NOT NULL,
    instance_id      TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'allocated',
    note             TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT supply_ledger_supplier_user_fkey
        FOREIGN KEY (supplier_user_id)
        REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT supply_ledger_user_fkey
        FOREIGN KEY (user_id)
        REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT supply_ledger_offer_owner_fkey
        FOREIGN KEY (offer_id, supplier_user_id)
        REFERENCES supply_offers(id, supplier_user_id) ON DELETE CASCADE,
    CONSTRAINT supply_ledger_instance_owner_fkey
        FOREIGN KEY (instance_id, user_id)
        REFERENCES instances(id, user_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_supply_ledger_offer ON supply_ledger (offer_id);
CREATE INDEX IF NOT EXISTS idx_supply_ledger_user ON supply_ledger (user_id);
