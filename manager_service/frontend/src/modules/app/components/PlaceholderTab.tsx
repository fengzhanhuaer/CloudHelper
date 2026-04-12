type PlaceholderTabProps = {
  title: string;
  description: string;
};

export function PlaceholderTab(props: PlaceholderTabProps) {
  return (
    <div className="content-block">
      <h2>{props.title}</h2>
      <p>{props.description}</p>
    </div>
  );
}
