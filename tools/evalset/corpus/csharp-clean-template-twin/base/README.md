# Reports

Sales exports of the example shop. Each export is a job that turns the sales
rows of a day into a text file:

- `Exports` – the export base class and the shop's own CSV and TSV exports
- `Partners` – exports in formats that partners prescribe
- `Sales` – where the rows come from

`ExportRunner` writes one file per job into the outbox directory.
