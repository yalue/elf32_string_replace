32-bit ELF String Replacement
=============================

About
-----

This defines a command-line tool for replacing string table entries in compiled
32-bit ELF files, without breaking the ELF format. Note that this only keeps
the ELF file *format* consistent--which doesn't mean that the resulting library
or executable will still be able to run! It was intended to be used for a
specific purpose: changing the names of shared library dependencies
post-compilation. This technique was used for part of a research paper found
[at this link](https://cs.unc.edu/~anderson/papers/rtas17a.pdf).

Example Usage
-------------

The following example was tested on a Raspberry Pi Model B, running Raspbian
GNU/Linux version 8.0:

```bash
# Create copies of the shared library we wish to re-link
cd /lib/arm-linux-gnueabihf/
sudo cp libc-2.19.so libc_copy-2.19.so
sudo ln -s -T /lib/arm-linux-gnueabihf/libc_copy-2.19.so libc_copy.so.6

# Create a copy of bash that uses the library copy
cd ~
./elf32_string_replace -file /bin/bash -output new_bash \
  -to_match 'libc\.so' \
  -replace libc_copy.so

# Test the new copy of bash to make sure it loaded the libc copy.
./new_bash
pmap -x $$ | grep libc

# This above should output something like the following lines:
# b6ddd000    1196    1132       0 r-x-- libc_copy-2.19.so
# b6ddd000       0       0       0 r-x-- libc_copy-2.19.so
# b6f08000      64       0       0 ----- libc_copy-2.19.so
# b6f08000       0       0       0 ----- libc_copy-2.19.so
# b6f18000       8       8       8 r---- libc_copy-2.19.so
# b6f18000       0       0       0 r---- libc_copy-2.19.so
# b6f1a000       4       4       4 rw--- libc_copy-2.19.so
# b6f1a000       0       0       0 rw--- libc_copy-2.19.so
```

Compiling the program
---------------------
The program can be built using the go programming language. First install the
go compiler, then run `go install github.com/yalue/elf32_string_replace`.

Alternatively some pre-built versions are available on the [releases page for
this project](https://github.com/yalue/elf32_string_replace/releases).

How ELF strings are replaced
============================

 1. Identify all string table sections.

 2. For each string in each string array, see if it matches the search regex.
    If so, record its original index, perform the replacement, and append the
    new replacement to the end of the table. New and old locations of tables
    are recorded in `replacedStringTable` structures in the code, and
    individual string locations are recorded in `replacedString` structures.

 3. Replace all string table references (locations listed below) with offsets
    into the newly rebuilt string tables, if the referenced string was changed.
    This is easy to do by checking each string's offset into the correct table
    against the list of `replacedString`s for that table.

 4. Rewrite any hash tables? This step is actually not carried out, since hash
    values used in compiled code will still refer to the original symbol names.
    Therefore, this step is actually not carried out for now.

 5. Append the new string table sections to the end of the file. Steps 5-9 are
    carried out in the `relocateStringTables` function in the code.

 6. Change the offset and length of the original string table section headers
    to refer to the locations and sizes of the updated string tables (now at
    the end of the file). Update the Virtual Address fields in addition to the
    file offsets.

 7. Add a new loadable read only data segment that will encompass the string
    tables at the end of the file, in addition to a list of updated program
    headers. The list of program headers will contain one more entry (this one)
    than the original program headers. Make sure it uses the correct virtual
    address and file offsets referring to the start of the newly appended
    string tables.

 8. Write the new program header table to the end of the file, after the string
    tables. Update the program headers segment (within this table) to point to
    the new location and size of this table.

 9. Update the ELF file header's program header table offset and sizes to
    the values for the newly appended copy.

 10. Write the result to the new output ELF file.

Known fields which refer to string table entries
------------------------------------------------

 - The "Name" field in section headers

 - The "Name" field in symbol table entries

 - The "Name" field in ELF32Verdaux structures, in the `.gnu_version_d`
   sections. (The version symbol table contains 16-bit entries showing only
   local, global, or user-defined scope).

 - In `.gnu_version_r` sections:

    - The "VNFile" field in ELF32Verneed structures

    - The "Name" field in ELF32Vernaux structures

 - In the dynamic section:

    - The values with the needed tags (tag = 1)

    - The shared object name (tag = 14)

    - The library search path (tag = 15)

 - Hash table sections must be rebuilt if symbol names are changed. To do this,
   take the original hash table section and parse out the headers, etc. Then,
   rebuild the hash table using the same number of buckets and so on, but use
   the current symbol names.

 - GNU hash table sections must also be rebuilt if present.

Fields which *may* refer to strings, pending further investigation
==================================================================

 - The "Value" field in symbol table entries. If it contains the virtual
   address or offset of a string table entry, should it be adjusted?
