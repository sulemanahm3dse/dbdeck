# DBDECK v0.1.0

> A deck of panes for PostgreSQL · MySQL · SQLite · Redis

A fast, keyboard-driven **terminal database client** in Go for **PostgreSQL, MySQL/MariaDB, SQLite, and Redis**. 


A terminal database client written in Go. Browse a tree of schemas, tables, and keys; scroll large tables smoothly; edit cells safely; run queries in tabs; and use manual transactions—all entirely from the keyboard (with full mouse support).

```ascii
╭─ 1 Explorer ──────────╮╭─ 2 Data ───────────────────────────────────────────────────╮
│ / filter · ? help     ││ orders · 200 of 450 rows · row 1 · sort ↑id · 6.3ms         │
│ ▾ public              ││    id   │ status   │ amount   │ paid   │ created        █  │
│     orders            ││  1 1    │ pending  │ 1.50     │ false  │ 2026-10-24 …   │  │
│     v_done     (view) ││  2 2    │ done     │ 3.00     │ true   │ 2026-10-24 …   │  │
│ ▸ sales               │╰────────────────────────────────────────────────────────────╯
│                       │╭─ 3 Editor ─────────────────────────────────────────────────╮
│                       ││ Query  1  2   ctrl+r run · ctrl+t new tab · esc back        │
╰───────────────────────╯╰────────────────────────────────────────────────────────────╯
 NORMAL  TX 1 pending  hjkl move · s sort · / filter · e edit · ? help     postgres · …
 ```

## Run it

```bash
# 1. prebuilt (Linux x86_64) — nothing to install:
./bin/dbdeck-linux-amd64 --demo

# 2. or build from source (Go 1.26+; older Go downloads it automatically):
make build            # runs `go mod tidy`, then builds ./bin/dbdeck
./bin/dbdeck --demo                                    # in-memory sample data
./bin/dbdeck                                           # connection screen
./bin/dbdeck postgres://postgres:secret@localhost:5432/erd_forge
./bin/dbdeck mysql://user:pass@localhost:3306/shop
./bin/dbdeck redis://:pass@localhost:6379/0
./bin/dbdeck ./my.db                                   # SQLite file
```

Special characters in passwords must be percent-encoded in a URL (`@` → `%40`, `/` → `%2F`, `:` → `%3A`).
On the connection screen you can instead fill the fields — the URL and the fields stay in sync
both ways, so you can paste a URL or type the parts. The form hides the password in the URL it shows.

## Screens

**Connections** — saved connections list + form. `n` new (paste a URL), `e` edit, `d` delete, `enter` connect,
`D` demo database. In the form: `ctrl+t` tests the connection, `ctrl+s` saves, `enter` connects.
Profiles are stored in `~/.config/dbdeck/connections.toml` **without passwords**; passwords go to the OS keychain
(`libsecret`/GNOME Keyring, macOS Keychain, Windows Credential Manager). If no keychain is available you are
asked for the password when you connect.

**Browser** — Explorer (tree), Data (grid), Editor (query tabs). Press `?` for every key binding.

| Where | Keys |
|---|---|
| everywhere | `tab`/`shift+tab` or `alt+1/2/3` switch pane · `i` editor · `T` transaction menu · `ctrl+s` commit · `ctrl+o` back to connections · `?` help · `q` quit |
| scrolling | `j/k` `ctrl+d/u` `ctrl+f/b` `PgUp/PgDn` `g/G` · mouse wheel · `G` loads to the true last row |
| explorer | `enter`/`l` open or expand · `h` collapse · `/` filter (enter on Redis searches **all** keys) · `r` reload |
| data | `hjkl` move · `0`/`$` first/last column · `s` sort asc→desc→off · `/` filter · `enter` inspect · `e` edit · `a` add row · `x` delete · `y`/`Y` copy cell/row · `f` freeze first column |
| editor | `ctrl+r` run · `ctrl+e` explain · `ctrl+t`/`ctrl+w` new/close tab · `alt+←/→` switch tab · `alt+↑/↓` history · `tab` complete · `esc` back |

## Screenshots

![Connections](docs/screenshots/connections.png)
*Connection management screen*

![New Connection](docs/screenshots/new-connection.png)
*Create connections via form or URL*

![Browser](docs/screenshots/browser-explorer.png)
*Explore schemas and tables*

![Data Grid](docs/screenshots/data-view.png)
*View and edit data*

### Scrolling
Every list uses one `Scroller` (cursor, offset, scroll-off of 2 rows, page/half-page jumps, wheel). The grid is virtualised —
only visible rows/columns are rendered — and table browsing loads **200 rows at a time**, fetching the next chunk
automatically when you get within 40 rows of the end (up to 50,000 loaded rows; filter to narrow). Wide tables scroll
horizontally with the selected column always kept in view.

### Editing & transactions
* Cells are edited with `e` (`ctrl+n` makes the value NULL). The row is identified by its **primary key**; tables without one,
  and views, are read-only. Every change must affect exactly one row or it is rolled back.
* **Auto mode** applies each edit immediately. **Manual mode** (`T` → `m`) keeps all edits and query writes pending —
  the status bar shows `TX n pending` — until you commit (`ctrl+s`) or roll back. A failing statement inside a manual
  transaction does not poison it (savepoints). Quitting or disconnecting with pending work asks what to do.
* Redis: strings, hashes, lists, sets and sorted sets are editable (`SET … KEEPTTL`, `HSET`, `LSET`, `ZADD XX`, `SREM`+`SADD`),
  streams are read-only; transactions don't exist there.

## Architecture

```
cmd/dbdeck/             entrypoint, CLI arguments
internal/app/          shell: connection screen <-> browser
internal/screens/      connections/ (list + form)   browser/ (explorer, data view, query tabs, status bar)
internal/widgets/      Scroller, Grid, Menu/Inspector/Form overlays, panes
internal/keymap/       every key binding; help text is generated from it
internal/driver/       Driver / Editor / Transactor / Searcher / Hinter interfaces + registry
  sqldb/               ONE implementation of paging, editing, transactions, savepoints on database/sql
  sqlite postgres mysql/   a Dialect each (quoting, placeholders, catalog queries)
  redisdrv/            keys as a tree grouped on ':', every type shown as a table, console
internal/config/       profiles, URL <-> fields, TOML store, keychain
```

Rules the code follows: no database call ever runs in `Update` (each is a `tea.Cmd` with a context and a
sequence number, so stale replies are dropped and `ctrl+c` cancels a running query); every widget renders to exactly the
size its parent gives it; drivers are used only through interfaces, so the UI never imports a database package.

**Adding an engine:** implement `driver.Driver` (and `Editor`/`Transactor` if it can), call `driver.Register` in `init()`, add a
blank import in `cmd/dbdeck/main.go`. For another SQL database you only write a `sqldb.Dialect`.

## Testing

```bash
make test                      # unit + flow tests, no servers needed
make test-integration PG=postgres://u:p@localhost:5432/scratch MYSQL=mysql://u:p@localhost:3306/scratch
```
The integration tests **drop and recreate** `orders`, `nopk`, `pairs`, `v_done`… in the database you point them at — use a scratch database.
What exists: scroller and grid unit tests (exact sizing, wide characters, 100k rows); driver tests for SQLite, Postgres, MySQL and
Redis (types, paging, filters, edits with composite/no keys, transactions + savepoints, cancellation, auth failures);
screen-flow tests that press keys and click with the mouse, and check that every rendered frame is exactly the terminal size.