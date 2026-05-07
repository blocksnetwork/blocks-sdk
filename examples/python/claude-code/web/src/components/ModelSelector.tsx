import type { ModelOption } from '../types.ts';

interface Props {
  value: ModelOption;
  onChange: (model: ModelOption) => void;
  disabled: boolean;
}

const MODEL_LABELS: Record<ModelOption, string> = {
  sonnet: 'Sonnet',
  opus: 'Opus',
  haiku: 'Haiku',
};

export default function ModelSelector({ value, onChange, disabled }: Props) {
  return (
    <select
      className="model-selector"
      value={value}
      onChange={(e) => onChange(e.target.value as ModelOption)}
      disabled={disabled}
    >
      {(Object.keys(MODEL_LABELS) as ModelOption[]).map((key) => (
        <option key={key} value={key}>
          {MODEL_LABELS[key]}
        </option>
      ))}
    </select>
  );
}
