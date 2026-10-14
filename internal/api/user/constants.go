package user

// passwordCredentialPrimaryKey is the constraint a duplicate credential loses to.
//
// It is the table's primary key, declared in migrations/000005 as
// `user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE`, which makes
// it the same thing as a unique constraint on user_id and therefore the only
// possible conflict target for CreatePasswordCredential.
//
// Naming it here rather than spelling the string at the point of use keeps the
// mapping from driver error to response code in one place: the SQL names the
// constraint it creates and the code names the constraint it refuses on, and
// those two are the same identifier by construction.
//
// It is matched against pgconn.PgError.ConstraintName rather than against the
// error text, for the reason the collection feature already relies on when it
// maps saved_items_collection_id_fkey.
const passwordCredentialPrimaryKey = "password_credentials_pkey"

// No rate-limiting constants are declared here yet, and their absence is a
// decision rather than an oversight.
//
// Both password routes perform at least one Argon2id derivation, so both will
// want a per-user budget for the reason security.PasswordWorkLimiter does not
// provide one: that limiter bounds how many derivations run at once, and a caller
// holding every slot and re-acquiring on release can hold them indefinitely while
// never exceeding a limit that is never tripped.
//
// When that budget is added it takes its own namespace, separate from every other
// flow's, for the rule this repository already follows: a namespace is one
// flow's budget, the limiter fails closed, and a shared counter would let one
// exhausted flow take another offline with it. It also has to decide whether a
// *failed* change is charged differently from a successful one, which is what
// login's login-email budget already does and a flat budget would not.
//
// Until then these routes are bounded only by the shared password-work limiter and
// by the authentication they require, which is the same position the collection
// and search routes are in: reads and writes that spend nothing beyond an
// ordinary query or one bounded derivation.
