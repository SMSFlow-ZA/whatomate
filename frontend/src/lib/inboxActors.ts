interface NamedActor {
  id: string
  full_name?: string
}

// A missing directory entry does not prove that an actor left or was deleted.
// Use only the current profile or names the caller is already allowed to read.
export function inboxActorName(
  id: string,
  current?: NamedActor | null,
  users?: readonly NamedActor[],
) {
  if (!id) return undefined
  const own = current?.id === id ? current.full_name?.trim() : undefined
  return (
    own || users?.find((user) => user.id === id)?.full_name?.trim() || undefined
  )
}
