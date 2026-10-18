# Prompt 网站实施记录

执行用户已批准的 Go + React + SQLite 首版方案。部署为私人网站；优化同步执行，断线取消；仅另存，不覆盖原条目；没有后台任务、通用版本历史或站内评测平台。用户追加：主 agent 只调度，后端 Pi / bfc/ark/deepseek-v4.1-flash，前端 Pi / cpa/claude-sonnet-5-5，串行实现；Codex GPT-6.1 Sol 子代理只读审阅；前端依赖改用 Bun，提交 bun.lock。

## 绑定的产品要求

- Prompt 库：标题、正文、标签、时间、可选来源运行 ID；增删改查、服务端标题/正文子串搜索、标签筛选、更新时间倒序、原样复制、载入优化。纯文本，不做变量填写。
- 方案：名称与正文增删改，工作台内管理。
- 工作台：原文/结果并排（窄屏上下）、字符差异与长度变化、检测 {{变量}} 增加/丢失警告，结果可编辑/复制/另存。只另存，不覆盖；保存用户修改正文，同时保留模型原始结果。旧请求不可覆盖切换后的输入或方案。
- 模型：一个后端环境配置的 OpenAI 兼容 /chat/completions，非流式文本，不要求 JSON mode。固定指令要求保留原意/硬约束，缺失事实用占位符、不编造；方案与原文分别传入。90 秒超时，客户端断线取消，无自动重试，失败保留输入。
- 运行：请求 ID、原文、方案名称/正文快照、模型、实际参数、原始结果、状态、错误类型、起止时间。数据库唯一请求 ID 防重，仅插入成功者调用上游；重复请求返回现有状态/结果。成功先落库再响应。启动时遗留 running 标为 interrupted。事务不跨模型调用。
- API：/api/session 登录/查询/退出；/api/prompts 列表/创建与 /{id} 详情/更新/删除；/api/schemes 列表/创建与 /{id} 详情/更新/删除；POST /api/optimizations 同步执行；GET /api/optimizations/{requestId} 查询丢失响应，无持续轮询。
- 安全：单密码环境配置、随机服务端会话、24 小时过期/重启失效；生产 HttpOnly+Secure+SameSite=Strict；写请求同源、登录限速；业务 API 鉴权。模型密钥留后端；日志不记录正文/密钥/原始错误响应。分类鉴权失败、限流、超时、无效响应、上游错误。
- Go 标准库 HTTP + database/sql + modernc.org/sqlite，无 ORM；单连接 SQLite，持久化目录；React/TypeScript/Vite，无状态库，无复杂 UI 框架；字符差异可用 diff。
- 生产 Go 二进制内嵌前端；Bun 管理前端依赖/脚本；Go Modules 正常下载，用户已准许本项目不 vendor。用户追加：本机容器验证改用 Apple container（已安装 CLI 1.0.0），不使用 OrbStack/Docker daemon；交付标准 OCI Dockerfile，并写 Apple container 的构建运行命令。服务器 compose 可作为可选部署方式。
- 交付容器、数据卷、HTTPS 反代示例、停服务备份/恢复/升级说明。演示数据主动导入、明确标注。
- 独立小型 JSONL 评测脚本：原始/优化 prompt 在同一任务模型上固定输入参数各跑 3 次，保留完整配置/输出；可执行检查先行；主观 A/B 随机盲评；报告通过数/胜负/持平，包含退化。模型可与优化器不同；演示不能证明效果。

## 验收映射

| 要求 | 所属任务 | 最小证据 |
|---|---|---|
| 登录/同源/CRUD/持久化 | 1 后端 | httptest，重新打开 SQLite 后原数据在 |
| 防重/快照/超时/取消/错误/重启中断 | 1 后端 | 模拟模型，竞态测试，并发同请求 ID 只调用一次 |
| 搜索复制/编辑另存/警告/旧结果隔离 | 2 前端 | TypeScript + Vite 构建 + Playwright 连模拟模型 |
| 内嵌与部署/主动演示导入/备份 | 3 交付 | 构建二进制与 HTTP smoke、配置静态检查 |
| 评测脚本完整输出/计数/盲评 | 3 交付 | 模拟模型的 JSONL 演示运行 |

## 顺序和共享契约

1. 后端实现 agent 负责 go.mod/go.sum、internal/app/** 与 docs/api.md。保留并完善现有 store.go。提供 NewServer(store *Store, cfg Config, assets fs.FS) (http.Handler,error) 或等价简洁入口，nil assets 可供 API 测试；完成后在 docs/api.md 固定实际请求/响应结构，供前端读取。可测试配置允许更短的 timeout；生产保持 90 秒。先不创建依赖尚未生成前端产物的 embed 主程序。
2. 前端 agent 使用 Pi / cpa/claude-sonnet-5-5（用户指定），读取已审阅后端及 docs/api.md，负责 web/**、Bun 锁文件、Playwright 配置/用例、cmd/server 主程序与静态内嵌入口；构建前端后可 go test ./...，生产为单二进制。与后端仅做有明确集成需要的最小调整。
3. 交付 agent 负责 Dockerfile/compose、README/.env.example、scripts/** 和 fixtures/**；完善完整运行/验收入口。不得擅自上线/push。

每项实现完成后由 gpt-6.1-sol 子代理只读审阅；修复由原 Pi agent 完成，最后整体验证及全分支审阅。禁止并发实现，禁止子 agent 再派子 agent。

## 交付与验证

1. 后端：密码会话、同源检查、SQLite、Prompt/方案 CRUD、运行快照、唯一请求 ID 防重、模型超时取消。用 httptest 覆盖接口、持久化及失败路径。
2. 前端：库、方案管理、对照工作台、字符差异、占位符警告、编辑另存。构建并用浏览器测试端到端流程。
3. 交付：嵌入前端、容器与 HTTPS 配置、主动导入的演示数据、JSONL 评测脚本、备份恢复说明。
4. 整体验证：Go race tests、TypeScript/生产构建、Playwright、独立只读审查；修复实际问题。

## 状态

- 已检查：初始仓库只有 .gitignore；已有未跟踪 .cw/ 保持不动。
- 用户明确允许本项目采用常规 Go Modules / Bun，覆盖全局依赖源码入库约定；Bun 替换先前 pnpm。
- 后端基线：父 agent 已创建 go.mod 和尚未测试的 internal/app/store.go，属于本任务可修改代码。
- 任务 1 初稿提交：f4bc426412ca64972c442ca65381b58c8f71b999。Pi agent 7130d6d0-5991-4c5b-96ca-d4baae07933c，模型 pi/bfc/ark/deepseek-v4.1-flash（high）。报告 /tmp/prompts-web-task1-report.md；报告40测试（含子测试）race通过、vet通过。
- 任务 1 审阅中：Codex gpt-6.1-sol（high），native agent 01a14ef7-53a9-7b20-a1f0-bc2818e3387c / Tesla。差异 /tmp/prompts-web-task1.diff。
- 前端管理器已检查：Bun 1.4.3。
- 本机 Apple container system start 成功；容器验收使用 Apple 工具链。停止排查 OrbStack。
- 任务1共享契约澄清：优化请求仅 request_id/input/scheme_id。run 保存原始 scheme_id（无外键），幂等比较原始 input+scheme_id，重放不重新解析可变方案正文。方案快照只用于当次执行及回查，避免编辑/删除方案破坏幂等。
- 接口集成要求：缺少模型配置时静态库仍须可启动使用，优化明确不可用；主程序按 APP_ORIGIN=https 自动设置 CookieSecure=true，不增加独立部署开关。
- 任务1初审 Needs fixes：并发登录限速非原子、无模型配置拒绝启动、终态落库失败仍返回正常running、API文档缺202。已交回原Pi一次修复，并补回归；Cookie报告和登录鉴权总则文字同步纠正。Sol认可原子claim/快照/取消/重启中断/错误脱敏，无需加架构层。
- 任务1修复提交：6ffd8a43fa4f927669f360c6199d95b3ef435b98，报告48测试（含子测试）race及vet通过；Sol正在针对修复差异 /tmp/prompts-web-task1-fix.diff 复核。
- 任务1 scoped复核：三项解决；仅POST登录成功response漏model_configured（GET已有）。已交Pi补字段/两种登录断言，修正文档202条件，给存储故障测试加同步屏障。其余路径已获认可。
- 任务1补齐提交：3c02ec8f7a24bf7994f592c4431c07c52023a630，报告51含子测试race及vet通过；小差异 /tmp/prompts-web-task1-fix2.diff 已交同一Sol确认。DELETE登出response不必含模型能力，前端登录POST/GET足够。
- 用户重申前端需求：页面明显新增Prompt入口/表单，一条prompt多tag，点击tag分类筛选并可清除及结合关键词。后端已有tags数组和tag查询，无需新增数据模型；已补任务2交互和浏览器验收。
- 任务1 complete：BASE 6e83699 → HEAD 3c02ec8f7a24bf7994f592c4431c07c52023a630，Sol最终Approved。任务2前端准备启动，基线同3c02ec8。
- 任务2实现中：Pi / cpa/claude-sonnet-5-5（medium），agent 3c17e8f9-48d0-4bfb-8c2a-46b0aa5eb632；报告 /tmp/prompts-web-task2-report.md。
- 用户补充已有Prompt的tags也可编辑：载入/增加/移除/全部清空并持久化，UI分类立即同步；已发给Sonnet并纳入Playwright验收。
- 用户明确原有Prompt标题/正文也能网页编辑，不能存配置文件：实际Prompt和方案均SQLite持久化+API读写，前端禁止写死实际数据；运行配置仅密码/站点/模型连接。已发给Sonnet，编辑刷新持久化纳入e2e。
- 任务2初稿提交：7c34f8936219d85d78991cd9c629c89b06eeff32。报告Bun frozen install/typecheck/build、go vet/test-race/build和6个真实Go+SQLite+mock模型Playwright通过；开发proxy登录200/写入201。任务2 Sol审阅已发起，差异 /tmp/prompts-web-task2.diff；待parent检查截图。
- 任务2审阅者：native Codex gpt-6.1-sol high agent 01a14f1c-8571-77e2-94c1-a8008573f87c / Maxwell。Parent已打开查看library桌面、workbench桌面与窄屏三张实际截图，未发现截断或横向溢出；页面具备明显新增/编辑/标签入口。
- 任务2初审Needs fixes（5项）：另存focusId覆盖库dirty草稿；SourceRun串来源；切页未取消/失效inflight；重登录后失败加载不恢复；默认监听需127.0.0.1。已交原Sonnet一次修复和真实UI回归，其他已符合功能不重写。
- 用户要求Hallmark优化UI，已通过选择确认统一视觉：冷白＋钴蓝工具工作台，紧凑列表、清晰编辑区、突出原文结果对照。随后明确UI设计/代码全部让Claude执行，parent只调度/验收/Sol审阅。
- HallmarkUI任务交同一Pi Sonnet负责读技能/设计/实现；先把5项功能fix独立提交交Sol复核，随后UI独立commit，避免混diff。UI报告 /tmp/prompts-web-ui-report.md；设计记录/tokens由Claude产出，不由parent实现。
- 任务2功能fix提交 b809a9efa1b09b1cca2121922c4775c23df97445，报告5新增回归旧版RED/新版GREEN，总11e2e+typecheck/build/Go-vet/race/build通过；当前Sol针对 /tmp/prompts-web-task2-fix.diff 复核。Hallmark视觉尚未开始。
- 任务2 complete：Sol scoped复核五项全部ADDRESSED，规格/质量Approved，HEAD b809a9efa1b09b1cca2121922c4775c23df97445。
- Hallmark UI实施已正式启动：同一Pi Claude Sonnet agent 3c17e8f9-48d0-4bfb-8c2a-46b0aa5eb632，基线b809a9e，用户已确认冷白钴蓝；parent不做UI代码，仅调度/验收，完成后Sol审阅独立diff。
- Hallmark UI初稿提交39766a591574cd071ea1a01f39fa94883f60e163，报告12e2e+完整构建/Go检查通过，320/375/414/768/1280截图及适用对比度检查；未声称全58门槛通过。新design.md/tokens.css/.hallmark及本地字体；Sol新上下文审阅已发起，差异 /tmp/prompts-web-ui.diff。
- Hallmark UI审阅者：native Codex gpt-6.1-sol high agent 01a14f37-7218-7200-aeb4-1915db4d81cd / Pascal。Parent已打开新版library-1280/workbench-1280/edit-375/workbench-320四张实际截图，无明显溢出或截断。UI报告有1字节UTF8编码问题（SHA行），已用只读decode(errors=replace)读取完整内容并告知审阅者，commitSHA以git为准。
- Parent在39766a5独立验收：Bun frozen安装无变更、typecheck、build均exit0；go test -race ./...显示51 passed in 3 packages，go vet exit0；Playwright 12/12（27.4s）通过；git diff --check 6e83699通过。Node存在NO_COLOR/FORCE_COLOR环境提示，不影响退出码或断言，无“输出完全无警告”的宣称。
- Hallmark UI初审2项P2：cards overflow裁切键盘focus ring；320px合法48字符英文tag的移除按钮被挤出视口/root clip隐藏。交原Claude一次修复+实际可达/移除与focus回归；其余关键全局样式/业务/本地字体获认可，不扩大基线801px列宽polish。
- Hallmark UI修复提交e09ea59f8062508440b5717b6db664a8fd48a90c，报告typecheck/build/Go-build及14e2e通过，两个新回归基线RED/修后GREEN。focus ring内置；chip文字收缩、省略，计数/×固定可达；报告UTF8已修复。已交Sol针对 /tmp/prompts-web-ui-fix.diff 复核。
- Hallmark UI complete：Sol scoped规格/质量Approved，两项ADDRESSED，允许下一交付任务，HEAD e09ea59f8062508440b5717b6db664a8fd48a90c。
- 任务3交付正式启动，基线e09ea59，Pi / bfc/ark/deepseek-v4.1-flash high，负责Apple容器/部署/README/demo/eval；报告 /tmp/prompts-web-task3-report.md。仅这一个实现者，前端Claude已完成待命。
- 任务3 Pi agent：a842928b-d97b-4085-924d-dc2f19b6dccf（workspace wks_654c8d6a8cf8f1e4）。预拉取Bun1.4.3-alpine、Go1.26-alpine成功；Apple builder running。后续Sol任务3审阅及全分支集成审阅仍待执行。
- 任务3约束澄清：不引入运行时递归chown用户DATA_DIR的entrypoint/su-exec/gosu或无需求tzdata；非root镜像自身目录初始化，Apple bind mount用显式UID/GID或专用volume匹配，避免修改用户既有数据owner。
- 任务3评测纠偏：评测唯一调用路径为独立EVAL_*服务，执行JSONL中原始/优化Prompt对应的实际任务；不得再次走网站优化接口或引入site/direct双模式。固定相同任务输入/参数、各3次，离线评分汇总不得重跑计费请求。
- Parent在e09ea59独立重跑Playwright：14/14通过（28.6s），包含焦点环完整、320px长tag真实点击清空保存刷新两个新回归。
- 任务3提交f14cd0cf79bf8c557997c79b04bf239c719ee2af，Apple镜像prompts-web:local digest dc40ab87b278构建/运行、demo重入、24调用评测管线实测；报告 /tmp/prompts-web-task3-report.md。Sol任务审阅 Averroes / 01a14f5c-8850-7ef3-8a00-4ecfb810d4c9 正在只读审阅。
- Parent独立交付验收：新建 /tmp/prompts-parent-verify.JQzNbD 数据目录，Apple容器prompts-parent-verify-19081使用交付镜像。无模型时空库/鉴权/新建和编辑正文+多tag/筛选均通过；重启旧session失效，编辑内容和tags从挂载SQLite完整读回。demo首次6条+3方案，第二次0新增。容器已停止保留供复核，不删除测试数据。
- Parent独立评测验收：新端口19101 mock，4样本×两臂×3=24输出、12盲评对，参数一致；关闭mock后按模板items字段离线汇总2票=优化1胜/1平/10未评，未发模型请求。初次parent自建投票漏items包装导致0计数，按交付模板纠正后通过；无产品修改。git diff --check 6e83699通过。
- 任务3 Sol初审Needs fixes（4项P2）：快速开始origin不匹配无法登录；离线汇总读默认样本漏报自定义退化；空checks对象虚增通过；Compose备份没有实际命名卷命令。已交原Pi集中修复，脚本针对复现+专属卷备份恢复验证；不重跑无关Go/UI套件。
- 任务3修复d9d80eed16ef2fc18dad8725ec624f04c35e38a3：Sol scoped确认登录/自定义样本离线退化/空checks三项Approved。Parent发现备份补丁仍只覆盖Apple命名卷，已交Pi补Compose实际app-data卷的备份与新卷恢复切换文档；仅此项待复核。
- 任务3 complete：Compose补丁76c1873及5e697f65e6842c85a8563809522a8cce7318e24d补齐实际卷备份、新卷root恢复、持久override，Sol整体Approved。Apple恢复实跑通过，Compose无daemon仅静态解析，未部署。
- 全分支最终集成审阅已发起：fresh Codex GPT6.1 Sol，基线6e83699→5e697f6，差异 /tmp/prompts-web-final.diff；关注跨任务真实接口/发布风险，不重复已通过的局部审阅和套件。
- 最终集成审阅者Kierkegaard / 01a14f6a-de81-7712-ad3d-70d60f423fd6发现3项P2：反代所有访客共享登录限额；首次补查running后丢失ID且重试新调用；退出失败页面仍假退出。已接受，先原后端Pi修可信反代边界，再Claude修两个前端状态，严格串行实现，随后scoped复核。
- 后端集成fix e1ce2f77f0c6b23b0508d97be1c90623e7516452：默认不信任头、显式TRUST_PROXY配合Caddy覆盖X-Real-IP及app不发布端口；无效头拒绝登录不耗额。报告57含子测试race、vet/build真实独立exit0（已纠正早先tail管道的无效退出码证据）。报告 /tmp/prompts-web-proxy-fix-report.md，Sol scoped复核中。
- 后端集成fix Sol Approved；parent独立go test -race ./...57 passed in3packages/exit0及diff-check通过。原Claude现开始最后两项前端状态修复，brief /tmp/prompts-web-final-ui-brief.md，报告 /tmp/prompts-web-final-ui-report.md。
- Parent使用Apple container +官方caddy:2.10-alpine，对当前deploy/Caddyfile执行caddy validate（APP_DOMAIN=prompts.example.test、只读挂载），exit0/Valid configuration；未启动公网代理、未申请证书。校验临时容器--rm自行退出。
- 前端集成fix5c8a2e032410f88bddbaecf65325802f07e79859：pendingID保留/手动查询/未知状态禁新调用，退出失败可见提示并保留登录状态。报告 /tmp/prompts-web-final-ui-report.md；typecheck/build/go-build及16e2e通过，两个新增回归旧版RED/修后GREEN。切页取消回归改为先查询cancelled再重试，保留取消/迟到隔离断言。Sol最终scoped复核中，parent重建最新Apple镜像prompts-web:verified。
- Parent最终镜像验收：Apple container build prompts-web:verified成功，manifest-list f879b8a06de3037bc3325efa4644acbdd7de5d9e2f67d3ffa40d5f12292cbb63。新容器prompts-parent-final-19082仅绑定127.0.0.1，挂载之前测试DB，开启TRUST_PROXY。真实HTTP验证缺失IP头400、访客A错误8次后429但访客B正确密码200、原有正文多tag仍在、退出后业务401。返回JS与当前web/dist逐字节SHA256一致（f99acdec71d2be875407befd3d9e2b4fd3985cb270b6cac965a0f4a0f0100ad1），含手动恢复入口。容器已停止保留供复核，无模型/真实数据，未部署。
- 最后UI scoped：pending恢复Approved（含400/401/403/409前置拒绝不调模型）；退出尚漏会话已过期/其他标签退出后的DELETE401应清本地登录视图。已交原Claude最小401分支+真实共享会话回归，503/网络失败保留语义不变。前一镜像验证对应5c8a2e，待小fix后更新最终镜像。
- 退出401补丁0e4665697fd7f1881eddeef0dbcf4510b385cc5f，只App+回归；报告typecheck/build/go-build/17e2e通过，真实共享cookie失效路径旧RED新GREEN，交Sol最终确认。
- Parent更新Apple镜像prompts-web:verified至0e46656，manifest-list d0d347dd458ec988e68498ca91ce0efca19b537e22c3cd76a030a31229155913/build exit0；在新容器19083上用真实Chromium浏览器登录，另一独立HTTP client共享cookie结束会话，本页DELETE真实401后正确回登录页且刷新未登录，exit0。镜像JS/assets/index-CsU7PfLH.js与本地最新dist逐字节相同。测试容器已停止保留，无生产部署。
- 完成：Sol最终Whole-branch Approved（产品HEAD0e4665697fd7f1881eddeef0dbcf4510b385cc5f）。最终有效证据为57 Go race测试、17真实Go/SQLite/mock模型Playwright、Bun frozen/typecheck/build、Go vet/build、Apple最终镜像构建和HTTP/真实浏览器验证、Caddy配置真实解析、24调用独立评测及离线评分。未运行真实模型、未实跑Compose或公网HTTPS；不宣称优化效果已验证。仅本地feat/prompt-library分支提交，未push/merge/部署；原有.cw/保持未跟踪不动。
