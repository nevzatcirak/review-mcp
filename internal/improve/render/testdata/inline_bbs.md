**Assert the retry count**

Label: possible issue · Score: 9 of 10

The test asserts nothing; check the retry count.

```diff
-func TestRetries(t *testing.T) {}
+func TestRetries(t *testing.T) {
+	if retries != 3 {
+		t.Fatal("retries")
+	}
+}
```

=====
**Cap the retry delay**

Label: possible issue · Score: 8 of 10

Cap the delay so that a large retry count cannot stall the caller.

```diff
-delay := retries * 100
+delay := min(retries*100, 1000)
```

=====
**Cap the retry delay**

Label: possible issue · Score: 8 of 10

Cap the delay so that a large retry count cannot stall the caller.

```diff
-a := 1
-b := 2
-c := 3
+a, b, c := 1, 2, 3
```

=====
