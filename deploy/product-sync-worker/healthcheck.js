const fs = require('node:fs')
const { workerStatusIsHealthy } = require('./worker-utils')

try {
  const status = JSON.parse(fs.readFileSync(process.env.STATUS_FILE || '/data/status.json', 'utf8'))
  process.exit(workerStatusIsHealthy(status) ? 0 : 1)
} catch {
  process.exit(1)
}
