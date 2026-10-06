<script setup lang="ts">
import { computed } from 'vue'

// Renders the Markdown subset used in GitHub release notes: headings, lists,
// tables, code blocks, paragraphs, and inline bold, code and links. Release
// notes come from GitHub, so everything is rendered as text nodes; there is
// no v-html.

const props = defineProps<{ source: string }>()

type Inline = { kind: 'text' | 'strong' | 'code'; text: string } | { kind: 'link'; text: string; href: string }

type Block =
  | { kind: 'heading'; level: number; content: Inline[] }
  | { kind: 'list'; items: Inline[][] }
  | { kind: 'table'; header: Inline[][]; rows: Inline[][][] }
  | { kind: 'code'; text: string }
  | { kind: 'paragraph'; content: Inline[] }

const INLINE_PATTERN = /\*\*(.+?)\*\*|`([^`]+)`|\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)|(https?:\/\/[^\s)<>]+)/g

function parseInline(text: string): Inline[] {
  const result: Inline[] = []
  let last = 0
  for (const match of text.matchAll(INLINE_PATTERN)) {
    if (match.index > last) result.push({ kind: 'text', text: text.slice(last, match.index) })
    if (match[1] !== undefined) result.push({ kind: 'strong', text: match[1] })
    else if (match[2] !== undefined) result.push({ kind: 'code', text: match[2] })
    else if (match[3] !== undefined) result.push({ kind: 'link', text: match[3], href: match[4] })
    else result.push({ kind: 'link', text: match[5], href: match[5] })
    last = match.index + match[0].length
  }
  if (last < text.length) result.push({ kind: 'text', text: text.slice(last) })
  return result
}

function tableCells(line: string): string[] {
  return line.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map((cell) => cell.trim())
}

const blocks = computed<Block[]>(() => {
  const lines = props.source.replace(/\r\n?/g, '\n').split('\n')
  const result: Block[] = []
  let index = 0
  while (index < lines.length) {
    const line = lines[index]
    const trimmed = line.trim()
    if (!trimmed) {
      index += 1
      continue
    }
    if (trimmed.startsWith('```')) {
      const code: string[] = []
      index += 1
      while (index < lines.length && !lines[index].trim().startsWith('```')) code.push(lines[index++])
      index += 1
      result.push({ kind: 'code', text: code.join('\n') })
      continue
    }
    const heading = /^(#{1,6})\s+(.*)$/.exec(trimmed)
    if (heading) {
      result.push({ kind: 'heading', level: heading[1].length, content: parseInline(heading[2]) })
      index += 1
      continue
    }
    if (trimmed.startsWith('|') && /^\|?\s*:?-{2,}/.test(lines[index + 1]?.trim() || '')) {
      const header = tableCells(trimmed).map(parseInline)
      const rows: Inline[][][] = []
      index += 2
      while (index < lines.length && lines[index].trim().startsWith('|')) rows.push(tableCells(lines[index++]).map(parseInline))
      result.push({ kind: 'table', header, rows })
      continue
    }
    if (/^[-*]\s+/.test(trimmed)) {
      const items: Inline[][] = []
      while (index < lines.length && /^\s*[-*]\s+/.test(lines[index])) {
        items.push(parseInline(lines[index].replace(/^\s*[-*]\s+/, '')))
        index += 1
      }
      result.push({ kind: 'list', items })
      continue
    }
    const paragraph: string[] = []
    while (index < lines.length && lines[index].trim() && !/^(#{1,6}\s|```|\||\s*[-*]\s)/.test(lines[index].trim())) {
      paragraph.push(lines[index].trim())
      index += 1
    }
    if (!paragraph.length) {
      // A line that only looks like a block start (e.g. a lone "|").
      paragraph.push(trimmed)
      index += 1
    }
    result.push({ kind: 'paragraph', content: parseInline(paragraph.join(' ')) })
  }
  return result
})
</script>

<template>
  <div class="release-notes">
    <template v-for="(block, blockIndex) in blocks" :key="blockIndex">
      <component :is="`h${Math.min(block.level + 2, 6)}`" v-if="block.kind === 'heading'">
        <template v-for="(part, partIndex) in block.content" :key="partIndex">
          <strong v-if="part.kind === 'strong'">{{ part.text }}</strong>
          <code v-else-if="part.kind === 'code'">{{ part.text }}</code>
          <a v-else-if="part.kind === 'link'" :href="part.href" target="_blank" rel="noopener noreferrer">{{ part.text }}</a>
          <template v-else>{{ part.text }}</template>
        </template>
      </component>
      <ul v-else-if="block.kind === 'list'">
        <li v-for="(item, itemIndex) in block.items" :key="itemIndex">
          <template v-for="(part, partIndex) in item" :key="partIndex">
            <strong v-if="part.kind === 'strong'">{{ part.text }}</strong>
            <code v-else-if="part.kind === 'code'">{{ part.text }}</code>
            <a v-else-if="part.kind === 'link'" :href="part.href" target="_blank" rel="noopener noreferrer">{{ part.text }}</a>
            <template v-else>{{ part.text }}</template>
          </template>
        </li>
      </ul>
      <div v-else-if="block.kind === 'table'" class="release-notes-table">
        <table>
          <thead>
            <tr>
              <th v-for="(cell, cellIndex) in block.header" :key="cellIndex">
                <template v-for="(part, partIndex) in cell" :key="partIndex">
                  <code v-if="part.kind === 'code'">{{ part.text }}</code>
                  <template v-else>{{ part.text }}</template>
                </template>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, rowIndex) in block.rows" :key="rowIndex">
              <td v-for="(cell, cellIndex) in row" :key="cellIndex">
                <template v-for="(part, partIndex) in cell" :key="partIndex">
                  <strong v-if="part.kind === 'strong'">{{ part.text }}</strong>
                  <code v-else-if="part.kind === 'code'">{{ part.text }}</code>
                  <a v-else-if="part.kind === 'link'" :href="part.href" target="_blank" rel="noopener noreferrer">{{ part.text }}</a>
                  <template v-else>{{ part.text }}</template>
                </template>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <pre v-else-if="block.kind === 'code'"><code>{{ block.text }}</code></pre>
      <p v-else>
        <template v-for="(part, partIndex) in block.content" :key="partIndex">
          <strong v-if="part.kind === 'strong'">{{ part.text }}</strong>
          <code v-else-if="part.kind === 'code'">{{ part.text }}</code>
          <a v-else-if="part.kind === 'link'" :href="part.href" target="_blank" rel="noopener noreferrer">{{ part.text }}</a>
          <template v-else>{{ part.text }}</template>
        </template>
      </p>
    </template>
  </div>
</template>
