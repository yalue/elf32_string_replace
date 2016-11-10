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

 1. Identify all string table sections. Each section contains a list of strings
    delimited by null bytes.

 2. For each string in each string array, see if it matches the search regex.
    If so, record its original offset into the table, perform the replacement,
    and append the post-replacement bytes to the end of a copy of the table.
    In the code, pre- and post-replacement string offsets are stored in
    `replacedString` structures, and the `replacedStringTable` structure
    tracks higher-level data about the table in which the strings were
    replaced.

 3. Replace all string references in other parts of the ELF file. The known
    locations which can refer to strings are listed below. ELF files refer to
    strings by specifying a string table section, and the offset into that
    section at which a specific string begins. For each string reference, the
    code makes use of the `replacedStringTable` and `replacedString` structures
    to see if a string has been replaced, and if so, what the new offset of the
    string should be.

 4. Potentially rewrite any hash tables. This step is actually not carried out,
    since hash values used in compiled code will still refer to the original
    symbol names. Hopefully this will always be okay, but this step is listed
    here anyway in case something breaks and re-building hash tables turns out
    to be necessary.

 5. Append the new string table sections to the end of the file. This step,
    along with steps 6-9, are carried out in the `relocateStringTables`
    function in the code.

 6. Change the offset and length of the original string table section headers
    to refer to the locations and sizes of the updated string tables (now at
    the end of the file). Update the Virtual Address fields in addition to the
    file offsets.

 7. Create a new loadable read-only segment to ensure that the string tables,
    now located at the end of the file, will actually be loaded into memory.
    This requires adding a new entry to the program header table, meaning that
    a modified copy of the program header table will also need to be appended
    to the end of the file. The new read-only segment can fill the dual
    purpose of loading both the relocated string table and program headers into
    memory.

 8. The program header table contains a self-referential entry called the
    program header segment. This entry, located in our modified copy of the
    program header table, must be updated to include the new virtual address,
    offset, and size of the table.

 9. Update the program header's file offset and size in the ELF file header.

 10. Write the result to the new output ELF file.

Known fields which refer to string table entries
------------------------------------------------

 - The "Name" field in section headers

 - The "Name" field in symbol table entries

 - The "Name" field in ELF32Verdaux structures, in the `.gnu_version_d`
   sections.

 - In `.gnu_version_r` sections:

    - The "VNFile" field in ELF32Verneed structures

    - The "Name" field in ELF32Vernaux structures

 - In the dynamic section:

    - The values with the needed tags (tag = 1)

    - The shared object name (tag = 14)

    - The library search path (tag = 15)

    - This isn't a string, but the dynamic section also contains an entry for
      a string table's virtual address which must be updated in line with the
      section relocation.

Fields which *may* refer to strings, pending further investigation
------------------------------------------------------------------

 - The "Value" field in symbol table entries. If it contains the virtual
   address or offset of a string table entry, should it be adjusted?

 - gcc appears to place all strings used in code in read-only data sections
   rather than ELF string tables. If this turns out to not be the case, and
   code sections contain hard-coded string offsets or virtual addresses, then
   they would need to be changed too. Hopefully that doesn't happen--it would
   make this project essentially impossible.
