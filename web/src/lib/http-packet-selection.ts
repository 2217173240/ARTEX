/** Display lines retain their offsets in the original packet, including mixed EOLs. */
export function packetLines(value: string) {
  const lines: { text: string; start: number }[] = [];
  const endings = /\r\n|\r|\n/g;
  let start = 0;
  for (const match of value.matchAll(endings)) {
    lines.push({ text: value.slice(start, match.index), start });
    start = match.index + match[0].length;
  }
  lines.push({ text: value.slice(start), start });
  return lines;
}

function packetOffset(root: HTMLElement, node: Node, offset: number, length: number) {
  for (const code of root.querySelectorAll<HTMLElement>("code[data-packet-start]")) {
    const start = Number(code.dataset.packetStart);
    const line = document.createRange();
    line.selectNodeContents(code);
    const position = line.comparePoint(node, offset);
    if (position < 0) return start;
    if (position === 0) {
      line.setEnd(node, offset);
      return start + line.toString().length;
    }
  }
  return length;
}

/** Only replace a selection wholly inside this packet; document selections stay native. */
export function selectedPacketText(root: HTMLElement, value: string, selection: Selection | null) {
  if (!selection || selection.isCollapsed || selection.rangeCount !== 1) return null;
  const range = selection.getRangeAt(0);
  if (!root.contains(range.startContainer) || !root.contains(range.endContainer)) return null;
  const start = packetOffset(root, range.startContainer, range.startOffset, value.length);
  const end = packetOffset(root, range.endContainer, range.endOffset, value.length);
  return value.slice(start, end);
}
