# 商品同步 ESA 挑战恢复记录

> 根因确认时间：2026-07-31。长期修复已集成到 `deploy/product-sync-worker`；未完成的一次性 `.esa-solver.js` 已删除。

## 根因

生产 Worker 曾被误配为 5 lane，且 `pay.ldxp.cn` 的阿里云 ESA 对全部代理出口返回浏览器滑块挑战。代理 TCP 链路、Worker 内存、主应用、PostgreSQL 与 Redis 均正常。挑战响应为 HTTP 200，但包含 ESA DOM/脚本以及 `x-tengine-error: denied by http_custom`，旧 Worker 只能将其判为错误并进入通用退避，因而所有 lane 最终失去浏览器 context。

## 长期修复

- 生产配置恢复为 6 lane：主出口 `17891-17896`，lane 1-3 的 fallback 为 `17897-17899`，每 lane `0.75 req/s`，全局 `4.5 req/s`，内存上限 `1.75g`。
- Worker 内置 `aliyun-esa` 与受验证页门控的 `generic-slide-to-end` provider，不再依赖一次性脚本、Puppeteer 或外部验证码服务。
- 每次按实际滑轨/滑块右边界计算距离，生成 36-60 步、900-1600ms 的非线性轨迹；每 context 最多两次，第二次前重新加载。
- 六 lane 共用可取消锁。挑战成功后重新访问首页并确认 DOM、文案和 `http_custom` 均消失；普通内容下的 `intelligent_cc_acl` 不误判。
- 启动时按 provider、origin 和代理身份恢复 Playwright `storageState`。会话原子写入 `/data/challenge-sessions/<sha256>.json`，目录 `0700`、文件 `0600`，损坏文件自动删除重建。
- 运行期 API 返回 HTML 时只暂停并重试失败的 API 调用，已完成的商品分类和报价不会重跑。当前 context 两次失败后立即尝试该 lane 的 fallback。
- 验证失败使用独立的 15 分钟、60 分钟、6 小时退避；网络和限流仍使用 1、5、15 分钟。无法识别的验证直接使用 6 小时层级。

## 生产变量

```env
PRODUCT_SYNC_CONCURRENCY=6
PRODUCT_SYNC_REQUEST_RATE_PER_LANE=0.75
PRODUCT_SYNC_PROXY_URLS=http://172.18.0.1:17891,http://172.18.0.1:17892,http://172.18.0.1:17893,http://172.18.0.1:17894,http://172.18.0.1:17895,http://172.18.0.1:17896
PRODUCT_SYNC_PROXY_FALLBACK_URLS=http://172.18.0.1:17897,http://172.18.0.1:17898,http://172.18.0.1:17899
PRODUCT_SYNC_WORKER_MEMORY_LIMIT=1.75g
PRODUCT_SYNC_CHALLENGE_AUTO_SOLVE=true
PRODUCT_SYNC_CHALLENGE_SESSION_DIR=/data/challenge-sessions
PRODUCT_SYNC_CHALLENGE_TIMEOUT_MILLISECONDS=90000
```

部署模板保持 `PRODUCT_SYNC_CHALLENGE_AUTO_SOLVE=false`，生产必须显式开启。验收和回滚步骤见 `deploy/product-sync-worker/README.md`。
