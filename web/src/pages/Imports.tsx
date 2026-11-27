import { useEffect, useRef, useState } from "react";
import { api, type Import, type Meta } from "../api";
import { timestamp } from "../format";

const today = () => {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
};

export function Imports({ meta, onImported }: { meta: Meta; onImported: () => void }) {
  const [imports, setImports] = useState<Import[]>([]);
  const [source, setSource] = useState("");
  const [asOf, setAsOf] = useState(today());
  const [busy, setBusy] = useState(false);
  const [over, setOver] = useState(false);
  const [result, setResult] = useState<Import>();
  const [error, setError] = useState<string>();
  const fileInput = useRef<HTMLInputElement>(null);

  const load = () => api.imports().then(setImports).catch((e) => setError(e.message));
  useEffect(() => {
    load();
  }, []);

  const upload = async (file: File) => {
    setBusy(true);
    setError(undefined);
    setResult(undefined);
    try {
      const im = await api.upload(file, source, asOf);
      setResult(im);
      load();
      onImported();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
      if (fileInput.current) fileInput.current.value = "";
    }
  };

  const remove = async (im: Import) => {
    if (!confirm(`Delete import #${im.id} (${im.filename}) and every transaction and position it added?`)) return;
    try {
      await api.deleteImport(im.id);
      load();
      onImported();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  return (
    <>
      <div className="card">
        <div className="card-head"><h2>Import data</h2></div>
        <div className="controls">
          <label className="sub">
            Source{" "}
            <select value={source} onChange={(e) => setSource(e.target.value)}>
              <option value="">Detect automatically</option>
              {meta.sources.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
          </label>
          <label className="sub">
            Positions as of <input type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} />
          </label>
        </div>
        <div
          className={`dropzone${over ? " over" : ""}`}
          role="button"
          tabIndex={0}
          onClick={() => fileInput.current?.click()}
          onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && fileInput.current?.click()}
          onDragOver={(e) => {
            e.preventDefault();
            setOver(true);
          }}
          onDragLeave={() => setOver(false)}
          onDrop={(e) => {
            e.preventDefault();
            setOver(false);
            const f = e.dataTransfer.files[0];
            if (f) upload(f);
          }}
        >
          {busy ? "Importing…" : `Drop a file here or click to choose (max ${meta.max_upload_mb} MB)`}
        </div>
        <input ref={fileInput} type="file" accept=".csv,.ofx,.qfx,text/csv" hidden onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])} />
        <ul className="sub" style={{ marginTop: 12, paddingLeft: 18 }}>
          {meta.sources.map((s) => <li key={s.id}><strong>{s.name}:</strong> {s.description}</li>)}
        </ul>
        {error && <p className="error">{error}</p>}
        {result && (
          <div className="notice">
            Imported <strong>{result.filename}</strong>: {result.transactions_inserted} new of {result.transactions_parsed} transactions
            {result.holdings_parsed > 0 && `, ${result.holdings_parsed} positions`}.
            {result.warnings.length > 0 && (
              <ul>{result.warnings.map((w, i) => <li key={i}>{w}</li>)}</ul>
            )}
          </div>
        )}
      </div>

      <div className="card">
        <div className="card-head"><h2>Import history</h2><span className="sub">Original files are kept in your encrypted database</span></div>
        {imports.length === 0 ? (
          <div className="empty">Nothing imported yet.</div>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr><th>#</th><th>File</th><th>Source</th><th>Imported</th><th className="num">New transactions</th><th className="num">Positions</th><th>Warnings</th><th /></tr>
              </thead>
              <tbody>
                {imports.map((im) => (
                  <tr key={im.id}>
                    <td className="muted">{im.id}</td>
                    <td><a href={`/api/imports/${im.id}/file`}>{im.filename}</a></td>
                    <td>{meta.sources.find((s) => s.id === im.source)?.name ?? im.source}</td>
                    <td>{timestamp(im.imported_at)}</td>
                    <td className="num">{im.transactions_inserted} / {im.transactions_parsed}</td>
                    <td className="num">{im.holdings_parsed}</td>
                    <td className="wrap muted">{im.warnings.length ? im.warnings.slice(0, 2).join("; ") + (im.warnings.length > 2 ? ` (+${im.warnings.length - 2} more)` : "") : "—"}</td>
                    <td><button className="btn danger" onClick={() => remove(im)}>Delete</button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}
