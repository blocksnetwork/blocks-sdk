interface Props {
  totalCost: number;
}

export default function CostTracker({ totalCost }: Props) {
  if (totalCost <= 0) return null;

  return (
    <span className="cost-tracker">
      Cost: ${totalCost.toFixed(2)}
    </span>
  );
}
