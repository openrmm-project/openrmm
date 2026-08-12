-- A registry for script categories.
--
-- A category used to exist only as a string repeated on each script, which meant
-- it could not exist before the first script was filed under it: there was
-- nothing to create, and nowhere for an empty category to live. Operators expect
-- to name a category first and file scripts into it afterwards.
--
-- scripts.category keeps the name rather than a foreign key. The registry is a
-- catalogue of names, not the owner of the relationship: a script written
-- straight through the API can still carry a name nobody registered, and listing
-- unions the two so such a category is never invisible. That keeps the Script
-- model, its API shape and every existing query unchanged.
CREATE TABLE IF NOT EXISTS script_categories (
    id         UUID PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Case-insensitive uniqueness: "Networking" and "networking" are the same
-- category to everyone except a byte comparison, and letting both exist would
-- reintroduce exactly the duplicate this feature is here to prevent.
CREATE UNIQUE INDEX IF NOT EXISTS script_categories_name_key
    ON script_categories (lower(name));

-- Adopt the categories already in use, so the registry starts out agreeing with
-- reality instead of looking empty next to a library full of filed scripts.
INSERT INTO script_categories (id, name)
SELECT gen_random_uuid(), category
  FROM (SELECT DISTINCT category FROM scripts WHERE category <> '') existing
 ON CONFLICT DO NOTHING;
