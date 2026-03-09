# BoxNex Project Memory

## Architecture
- Go backend (server/) + React frontend (web/src/)
- BoxNex platform, Project ID: 8c4416a4-5889-4700-a51d-5a1bce50b6ea
- server/main.go is the entry point; server/api/ has all HTTP handlers
- CompositeStore routes sessions/messages to local SQLite, projects/tasks/assets to remote

## Auth & Cloud Mode
- `ActivateCloud` / `DeactivateCloud` in server/api/auth.go switch store mode
- After ActivateCloud, `OnCloudActivated` callback starts pollChanges + larkChatWorker goroutines
- These goroutines were previously only started at startup if token existed; now started dynamically
- `DeactivateCloud` calls `OnCloudDeactivated` to stop them
- ensureDefaultProject handles ErrVersionConflict by finding/associating existing remote project

## Key Files
- server/api/auth.go – ActivateCloud, DeactivateCloud, ensureDefaultProject
- server/api/router.go – Server struct (has OnCloudActivated/OnCloudDeactivated callbacks)
- server/main.go – startCloudMode/stopCloudMode closures, poll goroutine lifecycle
- server/store/composite.go – hybrid routing (sessions local, projects remote)
- server/store/remote.go – HTTP client to remote data service

## SQLite 注意事项（SetMaxOpenConns(1)）
- store/db.go 设置了 SetMaxOpenConns(1)，所有 SQLite 操作共用一个连接
- **严禁嵌套查询**：`rows.Next()` 循环内（或 rows 未 Close 前）不能再执行任何 db.Query/Exec
  - rows 持有连接，嵌套查询会永久阻塞（自死锁）
- **rows 内勿做 I/O**：rows.Next() 循环内不要调用 os.Stat 等文件系统操作（持连接期间阻塞其他查询）
