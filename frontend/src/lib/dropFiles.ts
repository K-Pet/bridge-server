// readDataTransferFiles walks a DataTransfer payload (as supplied by a
// drop event) and returns every File contained inside it, recursing
// into any directory entries.
//
// Why this exists: e.dataTransfer.files only exposes top-level entries.
// If a user drops a folder containing subfolders, the browser surfaces
// the folder as a single DataTransferItem you have to descend into via
// the deprecated-but-universally-supported webkitGetAsEntry() API. The
// modern getAsFileSystemHandle() requires a permission prompt and is
// Chromium-only, so webkitGetAsEntry is the right choice for a web app
// that needs to "just work" across Safari/Firefox/Chrome on desktop.
//
// The function returns a flat array because the importer doesn't care
// about original directory structure — it routes every file by its
// embedded tags. We do preserve File.name (basename only) so the picker
// row shows a recognizable filename in the queue.

// Browser support split:
//   - DataTransfer.items + webkitGetAsEntry: every modern browser. Lets
//     us descend into folders. The "webkit" prefix is misleading — every
//     engine ships it under that name, including Firefox.
//   - DataTransfer.files: every browser, but flattens folders into a
//     0-byte entry the browser silently filters out.
// Prefer items; fall back to files for ancient targets.
export async function readDataTransferFiles(dt: DataTransfer): Promise<File[]> {
  if (dt.items && dt.items.length > 0) {
    const entries: FileSystemEntry[] = []
    for (let i = 0; i < dt.items.length; i++) {
      const item = dt.items[i]
      if (item.kind !== 'file') continue
      const entry = item.webkitGetAsEntry()
      if (entry) entries.push(entry)
    }
    if (entries.length > 0) {
      return walkEntries(entries)
    }
  }
  return Array.from(dt.files || [])
}

async function walkEntries(entries: FileSystemEntry[]): Promise<File[]> {
  // Walk in parallel — entries are independent. Per-folder reader
  // recursion below stays serial so a 10k-file album doesn't blow
  // the call stack.
  const out: File[] = []
  await Promise.all(entries.map(async (entry) => {
    if (entry.isFile) {
      const file = await entryToFile(entry as FileSystemFileEntry)
      if (file) out.push(file)
    } else if (entry.isDirectory) {
      const child = await readDirectory(entry as FileSystemDirectoryEntry)
      out.push(...child)
    }
  }))
  return out
}

function entryToFile(entry: FileSystemFileEntry): Promise<File | null> {
  return new Promise((resolve) => {
    entry.file(
      (f) => resolve(f),
      // Permission errors and missing-file races aren't worth surfacing —
      // dropping the entry quietly is what the browser would do anyway.
      () => resolve(null),
    )
  })
}

async function readDirectory(dir: FileSystemDirectoryEntry): Promise<File[]> {
  const reader = dir.createReader()
  const out: File[] = []

  // readEntries returns at most ~100 entries per call (Chromium caps
  // at 100; Safari less). Loop until it returns an empty batch so
  // very large folders are fully drained.
  while (true) {
    const batch: FileSystemEntry[] = await new Promise((resolve) => {
      reader.readEntries(
        (entries) => resolve(entries),
        () => resolve([]),
      )
    })
    if (batch.length === 0) break
    const flattened = await walkEntries(batch)
    out.push(...flattened)
  }
  return out
}
