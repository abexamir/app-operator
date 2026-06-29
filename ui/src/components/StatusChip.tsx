import Chip from '@mui/material/Chip'

const cfg: Record<string, { color: 'success' | 'info' | 'error' | 'warning' | 'default' }> = {
  Available:   { color: 'success' },
  Progressing: { color: 'info' },
  Failed:      { color: 'error' },
  Paused:      { color: 'warning' },
}

export function StatusChip({ phase }: { phase?: string }) {
  const label = phase ?? 'Unknown'
  const { color } = cfg[label] ?? { color: 'default' }
  return <Chip label={label} color={color} size="small" variant="outlined" />
}
