import { AGENT_OPTIONS } from '../types.ts';

interface Props {
  value: string;
  onChange: (agentName: string) => void;
  disabled: boolean;
}

export default function AgentSelector({ value, onChange, disabled }: Props) {
  return (
    <select
      className="agent-selector"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      disabled={disabled}
    >
      {AGENT_OPTIONS.map((opt) => (
        <option key={opt.agentName} value={opt.agentName}>
          {opt.label}
        </option>
      ))}
    </select>
  );
}
