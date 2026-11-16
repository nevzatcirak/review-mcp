using System.Collections.Generic;
using Example.Reports.Exports;

namespace Example.Reports.Partners;

public static class PartnerExports
{
    public static IReadOnlyList<ExportJob> All(string ledgerAccount) => new ExportJob[]
    {
        new ErpLedgerExport(ledgerAccount),
    };
}
