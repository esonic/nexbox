package store

import (
	"errors"
	"sync"
	"testing"
)

func TestOptionalAccountsPreserveWorkspace(t *testing.T) {
	s := newTestDB(t)
	legacy, err := s.CreateProject(CreateProjectRequest{Name: "existing", GitRepoPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	local, err := s.EnsureLocalUser()
	if err != nil {
		t.Fatal(err)
	}
	if enabled, err := s.HasLoginUsers(); err != nil || enabled {
		t.Fatalf("internal owner enabled login: %v, %v", enabled, err)
	}
	if users, err := s.ListUsers(); err != nil || len(users) != 0 {
		t.Fatalf("internal owner appeared in accounts: %v, %v", users, err)
	}
	if p, _ := s.GetProject(legacy.ID); p.OwnerUserID != local.ID {
		t.Fatal("existing account-free project was not adopted")
	}
	first, err := s.CreateUser("owner", "fixture-hash")
	if err != nil {
		t.Fatal(err)
	}
	if enabled, err := s.HasLoginUsers(); err != nil || !enabled {
		t.Fatalf("account did not enable login: %v, %v", enabled, err)
	}
	if p, _ := s.GetProject(legacy.ID); p.OwnerUserID != first.ID || p.GitRepoPath != legacy.GitRepoPath {
		t.Fatal("first account lost or moved existing work")
	}
	second, err := s.CreateUser("friend", "fixture-hash")
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := s.GetProject(legacy.ID); p.OwnerUserID != first.ID {
		t.Fatal("additional account stole the workspace")
	}
	if err := s.DeleteUser(first.ID); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := s.HasLoginUsers(); !enabled {
		t.Fatal("deleting one of two accounts disabled login")
	}
	if err := s.DeleteUser(second.ID); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := s.HasLoginUsers(); enabled {
		t.Fatal("last account removal did not restore local access")
	}
	if p, _ := s.GetProject(legacy.ID); p.OwnerUserID != local.ID || p.GitRepoPath != legacy.GitRepoPath {
		t.Fatal("returning to local mode lost or moved work")
	}
	if err := s.DeleteUser(local.ID); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("internal owner can be deleted as a login account: %v", err)
	}
	if err := s.UpdateUserPassword(LocalUsername, "fixture-hash"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("internal owner can be turned into a login: %v", err)
	}
}

func TestConcurrentLocalInitialization(t *testing.T) {
	s := newTestDB(t)
	var wg sync.WaitGroup
	ids := make(chan int64, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := s.EnsureLocalUser()
			if err != nil {
				errs <- err
				return
			}
			ids <- u.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first int64
	for id := range ids {
		if first == 0 {
			first = id
		}
		if id != first {
			t.Fatal("parallel requests created different local owners")
		}
	}
}
