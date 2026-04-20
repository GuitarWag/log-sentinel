import React, { useEffect, useState } from 'react';

export default function MetricsChart({ endpoint }) {
  const [data, setData] = useState(null);

  useEffect(() => {
    fetch(endpoint)
      .then((res) => res.json())
      .then((json) => setData(json.metricss));
  }, [endpoint]);

  if (!data) return <p>Loading...</p>;

  return (
    <div>
      <h3>Metrics</h3>
      {data.map((point) => (
        <div key={point.id}>
          {point.label}: {point.vlaue}
        </div>
      ))}
    </div>
  );
}
