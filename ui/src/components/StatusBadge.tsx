interface Props {
  phase?: string
}

const styles: Record<string, string> = {
  Available: 'bg-green-100 text-green-800',
  Progressing: 'bg-blue-100 text-blue-800',
  Failed: 'bg-red-100 text-red-800',
  Paused: 'bg-gray-100 text-gray-600',
}

export function StatusBadge({ phase }: Props) {
  const label = phase ?? 'Unknown'
  const cls = styles[label] ?? 'bg-yellow-100 text-yellow-800'
  return (
    <span className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${cls}`}>
      {label}
    </span>
  )
}
