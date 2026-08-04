import { Select, Tag } from 'antd'
import type { SelectProps } from 'antd'

// Provider inference is a display concern only: it colours the chip so a long
// model list stays scannable. Unknown models fall back to a neutral tag.
const PROVIDER_RULES: { match: RegExp; name: string; color: string }[] = [
  { match: /^(gpt|chatgpt|o[134]\b|o[134]-|dall-e|whisper|tts-|text-embedding|codex)/, name: 'OpenAI', color: '#10a37f' },
  { match: /claude|anthropic/, name: 'Claude', color: '#d97757' },
  { match: /gemini|gemma|learnlm|imagen|veo-/, name: 'Gemini', color: '#4285f4' },
  { match: /deepseek/, name: 'DeepSeek', color: '#4d6bfe' },
  { match: /qwen|tongyi/, name: 'Qwen', color: '#615ced' },
  { match: /glm|zhipu/, name: 'GLM', color: '#3859ff' },
  { match: /grok|xai/, name: 'Grok', color: '#1d1d1f' },
  { match: /kimi|moonshot/, name: 'Kimi', color: '#16191e' },
  { match: /doubao/, name: 'Doubao', color: '#2b5fd9' },
  { match: /llama/, name: 'Llama', color: '#0668e1' },
  { match: /minimax/, name: 'MiniMax', color: '#f23f5d' },
  { match: /mistral/, name: 'Mistral', color: '#fa520f' },
]

function modelProvider(model: string): { name: string; color: string } | undefined {
  const name = model.trim().toLowerCase()
  return PROVIDER_RULES.find((rule) => rule.match.test(name))
}

export interface ModelSelectProps {
  value?: string[]
  onChange?: (models: string[]) => void
  /** Suggested models shown in the dropdown; arbitrary names stay allowed. */
  candidates?: string[]
  placeholder?: string
  disabled?: boolean
}

/**
 * ModelSelect edits a model whitelist as removable chips. It uses tags mode so
 * an operator can type any model the suggestion list does not carry, which is
 * the common case for self-hosted or preview upstreams.
 */
export default function ModelSelect({
  value,
  onChange,
  candidates = [],
  placeholder,
  disabled,
}: ModelSelectProps) {
  const options: SelectProps['options'] = Array.from(new Set([...candidates, ...(value ?? [])])).map(
    (model) => ({ value: model, label: model }),
  )

  return (
    <Select
      mode="tags"
      allowClear
      disabled={disabled}
      style={{ width: '100%' }}
      placeholder={placeholder}
      value={value}
      onChange={(next) => onChange?.((next as string[]).map((model) => model.trim()).filter(Boolean))}
      options={options}
      tokenSeparators={[',', ' ', '\n']}
      maxTagCount="responsive"
      tagRender={({ label, closable, onClose }) => {
        const provider = modelProvider(String(label))
        return (
          <Tag
            color={provider?.color}
            closable={closable}
            onClose={onClose}
            style={{ marginInlineEnd: 4 }}
          >
            {String(label)}
          </Tag>
        )
      }}
    />
  )
}
