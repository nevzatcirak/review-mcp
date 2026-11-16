using System.Collections.Generic;

namespace Example.Pos.Printing;

public interface IPrinter
{
    void Print(IEnumerable<string> lines);
}
