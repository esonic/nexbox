package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"

	"golang.org/x/crypto/bcrypt"

	"github.com/esonic/nexbox/store"
)

// usernamePattern restricts usernames to safe-for-filesystem identifiers so a
// username can be embedded in <data-dir>/users/<username>/... without
// quoting. Length 1-32. The same charset is used for project slugs.
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// validUsername returns nil if the username matches the allowed pattern.
func validUsername(name string) error {
	if !usernamePattern.MatchString(name) {
		return fmt.Errorf("username must match %s", usernamePattern.String())
	}
	return nil
}

// runCLISubcommand dispatches admin subcommands like `nexbox add-user <name> <pw>`.
// It returns (handled, exitCode). When handled is false the regular server
// startup path takes over.
func runCLISubcommand(dataDir string) (bool, int) {
	if len(os.Args) < 2 {
		return false, 0
	}
	switch os.Args[1] {
	case "add-user", "del-user", "list-users", "reset-password":
		// fallthrough; handled below
	default:
		return false, 0
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "create data-dir: %v\n", err)
		return true, 1
	}
	db, err := store.New(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return true, 1
	}
	defer db.Close()
	s := store.NewSQLiteStore(db)

	args := os.Args[2:]
	switch os.Args[1] {
	case "add-user":
		return true, cmdAddUser(s, args)
	case "del-user":
		return true, cmdDelUser(s, args)
	case "list-users":
		return true, cmdListUsers(s)
	case "reset-password":
		return true, cmdResetPassword(s, args)
	}
	return true, 1
}

func cmdAddUser(s *store.SQLiteStore, args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "Usage: nexbox add-user <username> <password>")
		return 2
	}
	username, password := args[0], args[1]
	if err := validUsername(username); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	if password == "" {
		fmt.Fprintln(os.Stderr, "password must not be empty")
		return 2
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash password: %v\n", err)
		return 1
	}
	u, err := s.CreateUser(username, string(hash))
	if err != nil {
		if errors.Is(err, store.ErrUserExists) {
			fmt.Fprintf(os.Stderr, "user %q already exists\n", username)
			return 1
		}
		fmt.Fprintf(os.Stderr, "create user: %v\n", err)
		return 1
	}
	fmt.Printf("created user: id=%d username=%s\n", u.ID, u.Username)
	return 0
}

func cmdDelUser(s *store.SQLiteStore, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: nexbox del-user <username|id>")
		return 2
	}
	var (
		target *store.User
		err    error
	)
	if id, perr := strconv.ParseInt(args[0], 10, 64); perr == nil {
		target, err = s.GetUserByID(id)
	} else {
		target, err = s.GetUserByUsername(args[0])
	}
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			fmt.Fprintf(os.Stderr, "user not found: %s\n", args[0])
			return 1
		}
		fmt.Fprintf(os.Stderr, "look up user: %v\n", err)
		return 1
	}
	if err := s.DeleteUser(target.ID); err != nil {
		fmt.Fprintf(os.Stderr, "delete user: %v\n", err)
		return 1
	}
	fmt.Printf("deleted user: id=%d username=%s\n", target.ID, target.Username)
	return 0
}

func cmdListUsers(s *store.SQLiteStore) int {
	users, err := s.ListUsers()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list users: %v\n", err)
		return 1
	}
	if len(users) == 0 {
		fmt.Println("no users yet — create one with: nexbox add-user <username> <password>")
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tUSERNAME\tCREATED_AT")
	for _, u := range users {
		fmt.Fprintf(tw, "%d\t%s\t%s\n", u.ID, u.Username, u.CreatedAt.Format("2006-01-02 15:04:05"))
	}
	_ = tw.Flush()
	return 0
}

func cmdResetPassword(s *store.SQLiteStore, args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "Usage: nexbox reset-password <username> <new-password>")
		return 2
	}
	username, password := args[0], args[1]
	if password == "" {
		fmt.Fprintln(os.Stderr, "password must not be empty")
		return 2
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash password: %v\n", err)
		return 1
	}
	if err := s.UpdateUserPassword(username, string(hash)); err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			fmt.Fprintf(os.Stderr, "user not found: %s\n", username)
			return 1
		}
		fmt.Fprintf(os.Stderr, "reset password: %v\n", err)
		return 1
	}
	fmt.Printf("password reset for: %s\n", username)
	return 0
}

// defaultDataDir mirrors the logic in main.go for resolving the data directory
// when --data-dir is not specified. CLI subcommands run before flag.Parse() so
// they need their own copy of this resolution.
func defaultDataDir() string {
	if v := os.Getenv("NEXBOX_DATA_DIR"); strings.TrimSpace(v) != "" {
		return v
	}
	execPath, err := os.Executable()
	if err != nil {
		return "."
	}
	resolved, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		return filepath.Dir(execPath)
	}
	return filepath.Dir(resolved)
}
