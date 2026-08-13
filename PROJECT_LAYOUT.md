# tlog-web 项目结构（Vue3 + Vite + Element Plus / Go 单二进制）

```
tlog-web/
├── docker-compose.yml          # 服务编排（bind mount ./data → /data, ./recordings → /data/recordings）
├── Dockerfile                   # 多阶段：node 构建 frontend/dist → embed 进 Go 二进制
├── go.mod / go.sum
├── backend/                     # Go 后端代码
│   ├── server.go                # 入口 main()：openDB / migrate / collector.Run / http.ServeMux + 路由
│   ├── db.go                    # SQLite 会话索引读写：sessions 表（含 file_path，回放流已移出 DB 存文件）+ 清理逻辑
│   ├── collector.go             # harvester 采集（glob + checkpoint + 自愈）；ingest 追加写回放文件 <REC_DIR>/<rec>.cast（不分桶，一个会话一个文件）
│   └── replay.go                # tlog TIMING 双格式解析 + WS 回放（从 file_path 读物理文件逐行重放）
├── retention 逻辑               # 位于 server.go(retentionLoop/启动清理) + db.go(purgeExpiredSessions/scanOrphanRecordings)
├── frontend/                    # Vue3 + Vite + Element Plus 工程
│   ├── package.json
│   ├── vite.config.ts           # base:'./' outDir:'dist'
│   ├── index.html               # Vite 入口
│   └── src/
│       ├── main.ts              # 挂载 App + Element Plus
│       ├── App.vue              # 整体布局
│       ├── api/index.ts         # 封装 /api/sessions、/api/users
│       ├── types.ts             # Session / QueryParams 类型
│       ├── components/
│       │   ├── FilterBar.vue    # 日期范围 + 用户 + 搜索 + 每页条数
│       │   ├── SessionTable.vue # 会话列表 + 分页
│       │   └── ReplayDialog.vue # xterm.js 回放（WebSocket，binaryType=arraybuffer）
│       └── views/
│           └── SessionsView.vue # 组合 FilterBar + SessionTable
└── data/                        # bind mount → /data（tlog.db 索引, collector.state）
└── recordings/                  # bind mount → /data/recordings（回放实体文件，不分桶 <rec>.cast，受 RETENTION_DAYS 清理）
```

## 构建与部署
- `backend/` 的 Go 代码通过 `//go:embed frontend/dist` 打包前端产物（构建时把 frontend/dist COPY 进构建上下文）
- `vite.config.ts` 设 `base: './'` 保证 OpenResty 反代下资源相对路径加载
- Dockerfile 多阶段：node:22 构建 → golang:1.23 编译 → alpine 运行
- 部署：`docker compose build && docker compose up -d --force-recreate`
```
