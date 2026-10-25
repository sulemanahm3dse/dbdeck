// dbdeck: a terminal database client for PostgreSQL, MySQL, SQLite and Redis.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"dbdeck/internal/app"
	"dbdeck/internal/config"
	"dbdeck/internal/demo"
	"dbdeck/internal/driver"
	_ "dbdeck/internal/driver/mysql"
	_ "dbdeck/internal/driver/postgres"
	_ "dbdeck/internal/driver/redisdrv"
	_ "dbdeck/internal/driver/sqlite"
)

const version = "0.1.0"

const usage = `dbdeck — terminal database client (PostgreSQL, MySQL, SQLite, Redis)

usage:
  dbdeck                          open the connection screen
  dbdeck <url>                    connect directly, e.g.
                                   postgres://user:pass@localhost:5432/mydb
                                   mysql://user:pass@localhost:3306/mydb
                                   redis://:pass@localhost:6379/0
                                   sqlite:///path/to/file.db
  dbdeck <file.db>                open a SQLite file
  dbdeck --demo                   open an in-memory demo database
  dbdeck --version

saved connections live in ~/.config/dbdeck/connections.toml (passwords in the OS keychain).
press ? inside the app for every key binding.
`

func main() {
	var opts app.Options
	args := os.Args[1:]
	for len(args) > 0 {
		a := args[0]
		args = args[1:]
		switch {
		case a == "-h" || a == "--help":
			fmt.Print(usage)
			return
		case a == "--version" || a == "-v":
			fmt.Println("dbdeck", version)
			return
		case a == "--demo":
			p := config.Profile{Engine: config.EngineSQLite, Path: ":memory:"}
			d, err := driver.Open(context.Background(), p)
			if err == nil {
				err = demo.Seed(d)
			}
			if err != nil {
				fatal(err)
			}
			opts.Driver, opts.DriverP = d, p
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "unknown flag %s\n\n%s", a, usage)
			os.Exit(2)
		default:
			var p config.Profile
			if strings.Contains(a, "://") {
				var err error
				if p, err = config.ParseURL(a); err != nil {
					fatal(err)
				}
			} else {
				p = config.Profile{Engine: config.EngineSQLite, Path: a}
			}
			opts.Profile = &p
		}
	}

	model := app.New(opts)
	final, err := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	if m, ok := final.(*app.Model); ok {
		m.Close()
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "dbdeck:", err)
	os.Exit(1)
}
