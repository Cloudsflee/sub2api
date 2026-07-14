export interface VersionDisplay {
  compact: string
  buildLabel: string
}

export function versionDisplay(value: string): VersionDisplay {
  const normalized = value.trim().replace(/^v/i, '')
  const base = normalized.match(/^(\d+\.\d+\.\d+)/)?.[1]
  const customCommit = normalized.match(/-custom\.([0-9a-f]+)/i)?.[1]

  return {
    compact: base || truncateVersion(normalized),
    buildLabel: customCommit ? `custom ${customCommit.slice(0, 8)}` : '',
  }
}

function truncateVersion(value: string): string {
  if (value.length <= 18) return value
  return `${value.slice(0, 15)}...`
}
