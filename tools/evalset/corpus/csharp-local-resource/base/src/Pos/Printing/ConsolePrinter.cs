using System;
using System.Collections.Generic;

namespace Example.Pos.Printing;

/// <summary>Prints to the console; the stores use a thermal printer driver instead.</summary>
public sealed class ConsolePrinter : IPrinter
{
    public void Print(IEnumerable<string> lines)
    {
        foreach (var line in lines)
        {
            Console.WriteLine(line);
        }
    }
}
