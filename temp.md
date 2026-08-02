# 商品同步 ESA 挑战恢复记录

> 根因确认时间：2026-07-31。长期修复已集成到 `deploy/product-sync-worker`；未完成的一次性 `.esa-solver.js` 已删除。

## 根因

生产 Worker 曾被误配为 5 lane，且 `pay.ldxp.cn` 的阿里云 ESA 对全部代理出口返回浏览器滑块挑战。代理 TCP 链路、Worker 内存、主应用、PostgreSQL 与 Redis 均正常。挑战响应为 HTTP 200，但包含 ESA DOM/脚本以及 `x-tengine-error: denied by http_custom`，旧 Worker 只能将其判为错误并进入通用退避，因而所有 lane 最终失去浏览器 context。

## 生产诊断与未满足验收

- 2026-07-31 的生产配置已正确加载 6 lane、9 个 endpoint、每 lane `0.75 req/s` 和全局 `4.5 req/s`。Worker 保持 `healthy`、零重启，RSS 约 283 MiB，但状态仍为 `ready_lane_count=0`、`browser_context_count=0`、`browser_page_count=0`，六个 lane 均处于验证专用长退避；商品同步恢复、过期快照清除和两个完整周期验收尚未完成。
- 旧自动恢复能识别 `aliyun-esa`，并从当前 360px 滑轨和 40px 滑块动态计算出 320px 距离，但缺少按下前连续鼠标历史，因此 ESA 官方 verify 接口持续返回 `VerifyCode=F001`。2026-08-02 的同浏览器 A/B 已确认固定距离、DOM 定位、Camoufox 指纹及 X11 `isTrusted` 均不是剩余根因。
- 挑战恢复层已经在服务器直连出口自动通过；生产恢复当前首先依赖代理供应商入口恢复。诊断记录保留在服务器私有目录，临时脚本和截图不纳入正式镜像；在代理链路恢复且六 lane 完成同步验收前不得宣称商品同步已恢复。

## 2026-08-02 Camoufox 生产复测

- `50afef838` 已部署到生产。Worker 配置为 6 个持久 Camoufox lane，并会拒绝 Firefox 网络错误页；代理失败现在明确归类为未到达 `https://pay.ldxp.cn`，不再继续执行相对 URL `fetch`。
- 九个 Mihomo listener 已恢复为九个独立 gate 映射，但当前订阅中的 44 个 Shadowsocks 节点全部汇聚到 `52.196.144.61:2377`。该地址从生产机和独立网络均 TCP 超时；订阅仍有效且未耗尽，因此当前 6 lane 首先被代理供应商入口故障阻断，尚未到达 ESA。
- 最初两轮无代理、单 lane Camoufox 直连自动验证均返回 `F001`。随后通过 noVNC 记录同一服务器浏览器中的人工输入：第一次约 9.96 秒的慢拖失败；第二次成功拖动在按下前先于轨道右侧停留约 1.41 秒，再以约 40 个事件、1.44 秒接近滑块，静止约 1.18 秒后按下，并在约 1.12 秒到达轨道末端。
- 使用同一 Camoufox 指纹精确重放成功输入的 57 个拖动点及按下前路径后，ESA 自动放行。将其参数化为随机接近路径、平滑纵向弧线、后段加速、24-42px 越界移动和 550-850ms 释放前停留后，两个全新 profile（包含不同指纹种子）均在第一次自动拖动中通过，并加载正常商品接口。
- 正式 Worker 在人工验证实验期间暂停以控制内存峰值；部署新镜像后需要恢复。代理入口不可达时 Worker 仍会保持网络退避，不能将容器存活或直连挑战通过误报为商品同步恢复。

## 长期修复

- 生产配置恢复为 6 lane：主出口 `17891-17896`，lane 1-3 的 fallback 为 `17897-17899`，每 lane `0.75 req/s`，全局 `4.5 req/s`，内存上限 `1.75g`。
- Worker 内置 `aliyun-esa` 与受验证页门控的 `generic-slide-to-end` provider，不再依赖一次性脚本、Puppeteer 或外部验证码服务。
- 每次按实际滑轨/滑块右边界计算距离。按下前先在轨道右侧停留并用 30-42 个事件、1100-1700ms 平滑接近滑块，再静止 800-1400ms；按下后使用 36-60 步（默认 50-60）、主轨道 1000-1400ms 的实测后段加速曲线，越过夹紧后的轨道 24-42px，并在释放前停留 550-850ms。各阶段均按绝对时间点调度；每 context 最多两次，第二次前重新加载。
- 六 lane 共用可取消锁，每个 context 的 90 秒预算从取得锁后开始，排队仍可立即取消且不消耗求解预算。挑战成功后重新访问首页并确认 DOM、文案和 `http_custom` 均消失；普通内容下的 `intelligent_cc_acl` 不误判。
- 启动时按 provider、origin 和代理身份恢复 Playwright `storageState`。会话原子写入 `/data/challenge-sessions/<sha256>.json`，目录 `0700`、文件 `0600`，损坏文件自动删除重建。
- 会话文件保留 provider、目标 origin、代理服务器和不可逆代理身份摘要，不写入代理用户名或密码；摘要不匹配时自动丢弃并重建。
- Camoufox 指纹种子按 lane 与代理身份稳定生成，不再依赖重建后会变化的容器 `HOSTNAME`；同一 profile 在 Worker 重启后保持相同指纹，不同 lane/出口仍彼此隔离。
- 运行期 API 返回 HTML 时只暂停并重试失败的 API 调用，已完成的商品分类和报价不会重跑。当前 context 两次失败后立即尝试该 lane 的 fallback。
- 验证失败使用独立的 15 分钟、60 分钟、6 小时退避；网络和限流仍使用 1、5、15 分钟。无法识别的验证直接使用 6 小时层级。

## 生产变量

```env
PRODUCT_SYNC_CONCURRENCY=6
PRODUCT_SYNC_REQUEST_RATE_PER_LANE=0.75
PRODUCT_SYNC_PROXY_URLS=http://172.18.0.1:17891,http://172.18.0.1:17892,http://172.18.0.1:17893,http://172.18.0.1:17894,http://172.18.0.1:17895,http://172.18.0.1:17896
PRODUCT_SYNC_PROXY_FALLBACK_URLS=http://172.18.0.1:17897,http://172.18.0.1:17898,http://172.18.0.1:17899
PRODUCT_SYNC_WORKER_MEMORY_LIMIT=1.75g
PRODUCT_SYNC_WORKER_PIDS_LIMIT=1024
PRODUCT_SYNC_BROWSER=camoufox
CAMOUFOX_PATH=/opt/camoufox/camoufox
CAMOUFOX_FIREFOX_VERSION=152.0
PRODUCT_SYNC_CHALLENGE_AUTO_SOLVE=true
PRODUCT_SYNC_CHALLENGE_NATIVE_DRAG=true
PRODUCT_SYNC_CHALLENGE_NATIVE_DRAG_DEBUG=false
PRODUCT_SYNC_CHALLENGE_SESSION_DIR=/data/challenge-sessions
PRODUCT_SYNC_CHALLENGE_TIMEOUT_MILLISECONDS=90000
PRODUCT_SYNC_BROWSER_PROFILE_DIR=/data/browser-profiles
```

部署模板保持 `PRODUCT_SYNC_CHALLENGE_AUTO_SOLVE=false`，生产必须显式开启。验收和回滚步骤见 `deploy/product-sync-worker/README.md`。
