// This program may be used to modify string tables in compiled ELF binaries,
// (hopefully) without breaking functionality of the program or library. It is
// intended to be used primarily for replacing library dependencies, and may
// not work for other strings.
//
// Usage:
//    ./elf32_string_replace -file /bin/bash -output ./bash_modified \
//        -to_match "libc.so.6" -replace "libc_alternative.so.6"
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"github.com/yalue/elf_reader"
	"io/ioutil"
	"log"
	"os"
	"regexp"
	"strings"
)

// This tracks each string that was replaced, including old and new offsets
// into the string table.
type replacedString struct {
	originalOffset uint32
	newOffset      uint32
}

// This tracks each updated string table.
type replacedStringTable struct {
	oldContent        []byte
	newContent        []byte
	oldFileOffset     uint32
	newFileOffset     uint32
	oldVirtualAddress uint32
	newVirtualAddress uint32
	sectionIndex      uint16
	replacements      []replacedString
}

// Returns a string representation of the replacedString value at
// replacements[i]. This is mostly for logging/debugging, so the string values
// may be incorrect if the index or replacedStringTable structure contains any
// errors.
func (r *replacedStringTable) showReplacement(replacementIndex int) string {
	if replacementIndex > len(r.replacements) {
		return fmt.Sprintf("Invalid replacedString index %d", replacementIndex)
	}
	originalOffset := r.replacements[replacementIndex].originalOffset
	newOffset := r.replacements[replacementIndex].newOffset
	tmp, e := elf_reader.ReadStringAtOffset(originalOffset, r.oldContent)
	var originalString, newString string
	if e != nil {
		originalString = fmt.Sprintf("<error reading: %s>", e)
	} else {
		originalString = string(tmp)
	}
	tmp, e = elf_reader.ReadStringAtOffset(newOffset, r.newContent)
	if e != nil {
		newString = fmt.Sprintf("<error reading: %s>", e)
	} else {
		newString = string(tmp)
	}
	return fmt.Sprintf("%s -> %s", originalString, newString)
}

// Fills in the replacements and newContent slices in the replacedStringTable
// structure. The oldContent field must already be set before calling this. If
// no strings are replaced, the replacements and newContent fields will be set
// to nil, but no error will be returned. Otherwise, newContent will be set to
// a newly allocated string table with the replaced values, and replacements
// will contain the replaced string offsets.
func (t *replacedStringTable) doReplacements(regex *regexp.Regexp,
	replacement string) error {
	replacements := make([]replacedString, 0, 4)
	sectionStrings := strings.Split(string(t.oldContent), "\x00")
	var currentOldOffset uint32
	var newString string
	var replacementOffsets replacedString
	newContent := make([]byte, len(t.oldContent))
	copy(newContent, t.oldContent)
	tableChanged := false
	for _, oldString := range sectionStrings {
		newString = regex.ReplaceAllString(oldString, replacement)
		replacementOffsets.originalOffset = currentOldOffset
		currentOldOffset += uint32(len(oldString)) + 1
		if oldString == newString {
			continue
		}
		// New strings will be appended to the end of the table.
		replacementOffsets.newOffset = uint32(len(newContent))
		tableChanged = true
		replacements = append(replacements, replacementOffsets)
		newContent = append(newContent, []byte(newString)...)
		newContent = append(newContent, 0x00)
	}
	if !tableChanged {
		return nil
	}
	t.newContent = newContent
	t.replacements = replacements
	return nil
}

// Creates the list of string tables with replaced strings, and returns a slice
// of them. May return a nil or 0-length slice if no strings were replaced.
// Returns an error if one occurs.
func processReplacements(f *elf_reader.ELF32File, regex *regexp.Regexp,
	replacement string) ([]replacedStringTable, error) {
	toReturn := make([]replacedStringTable, 0, 1)
	var t replacedStringTable
	var section *elf_reader.ELF32SectionHeader
	var e error
	var sectionName string
	for i := range f.Sections {
		if !f.IsStringTable(uint16(i)) {
			continue
		}
		t = replacedStringTable{}
		t.sectionIndex = uint16(i)
		section = &(f.Sections[i])
		t.oldFileOffset = section.FileOffset
		t.oldVirtualAddress = section.VirtualAddress
		t.oldContent, e = f.GetSectionContent(uint16(i))
		if e != nil {
			return nil, fmt.Errorf("Failed reading section %d: %s", i, e)
		}
		e = (&t).doReplacements(regex, replacement)
		if e != nil {
			return nil, fmt.Errorf("Failed replacing strings in sec. %d: %s",
				i, e)
		}
		// Only keep track of sections where strings were actually replaced.
		if len(t.replacements) == 0 {
			continue
		}
		sectionName, e = f.GetSectionName(t.sectionIndex)
		if e != nil {
			log.Printf("Replaced strings in sec. %d (bad name: %s)\n", i, e)
		} else {
			log.Printf("Replaced strings in section %s\n", sectionName)
		}
		toReturn = append(toReturn, t)
	}
	return toReturn, nil
}

// Converts a given file offset to a virtual address, based on the base virtual
// address from the given section index.
func fileOffsetToVirtualAddress(f *elf_reader.ELF32File, sectionIndex uint16,
	offset uint32) (uint32, error) {
	if int(sectionIndex) > len(f.Sections) {
		return 0, fmt.Errorf("Invalid section index: %d", sectionIndex)
	}
	section := &(f.Sections[sectionIndex])
	return offset + (section.VirtualAddress - section.FileOffset), nil
}

// Returns the byte offset to the start of the section header in f.Raw.
func getSectionHeaderOffset(f *elf_reader.ELF32File,
	sectionIndex uint16) uint32 {
	return f.Header.SectionHeaderOffset + uint32(sectionIndex)*
		uint32(binary.Size(elf_reader.ELF32SectionHeader{}))
}

// Wraps elf_reader.WriteAtOffset for this particular ELF file. Remember that
// f.ReparseData must still be called later on.
func writeAtELFOffset(f *elf_reader.ELF32File, offset uint32,
	toWrite interface{}) error {
	var e error
	f.Raw, e = elf_reader.WriteAtOffset(f.Raw, uint64(offset), f.Endianness,
		toWrite)
	return e
}

// Appends new string tables (containing the replacements) to the end of the
// ELF file, relocating the original string table sections to point to the new
// tables. Sets the newFileOffset and newVirtualAddress fields in each of the
// replacedStringTable entries. Returns nil on success.
func relocateStringTables(f *elf_reader.ELF32File,
	newTables []replacedStringTable) error {
	if len(newTables) == 0 {
		return nil
	}
	// Align the end of the file to 8 bytes
	for (len(f.Raw) % 8) != 0 {
		f.Raw = append(f.Raw, 0)
	}
	originalEndOffset := uint32(len(f.Raw))
	originalEndVA, e := fileOffsetToVirtualAddress(f,
		newTables[0].sectionIndex, originalEndOffset)
	if e != nil {
		return fmt.Errorf("Couldn't calculate ELF file end VA: %s", e)
	}
	// Start by appending all of the tables to the end of the file
	currentFileOffset := originalEndOffset
	currentVirtualAddress := originalEndVA
	var newContentLength uint32
	var t *replacedStringTable
	var section *elf_reader.ELF32SectionHeader
	for i := range newTables {
		t = &(newTables[i])
		t.newFileOffset = currentFileOffset
		t.newVirtualAddress = currentVirtualAddress
		f.Raw = append(f.Raw, t.newContent...)
		newContentLength = uint32(len(t.newContent))
		currentFileOffset += newContentLength
		currentVirtualAddress += newContentLength
		// Update the size, virtual address, and file offset in the section
		// header for the original string table.
		section = &(f.Sections[t.sectionIndex])
		section.VirtualAddress = t.newVirtualAddress
		section.FileOffset = t.newFileOffset
		section.Size = newContentLength
	}
	// Write the (potentially) modified section headers back into the ELF file
	// content.
	e = writeAtELFOffset(f, f.Header.SectionHeaderOffset, f.Sections)
	if e != nil {
		return fmt.Errorf("Error updating section headers: %s", e)
	}
	// Pad to 8-byte alignment again before appending the new program header
	// segment, too. (The program header segment will overlap with the new
	// loadable string table segment, so that it actually gets loaded.)
	stringTableSegmentSize := currentFileOffset - originalEndOffset
	for (len(f.Raw) % 8) != 0 {
		f.Raw = append(f.Raw, 0)
		currentVirtualAddress += 1
		currentFileOffset += 1
		stringTableSegmentSize += 1
	}
	// Create a new segment which will hold the updated string tables.
	newSegment := elf_reader.ELF32ProgramHeader{
		Type:            elf_reader.LoadableSegment,
		FileOffset:      originalEndOffset,
		VirtualAddress:  originalEndVA,
		PhysicalAddress: 0,
		FileSize:        stringTableSegmentSize,
		MemorySize:      stringTableSegmentSize,
		Flags:           2,
		Align:           8,
	}
	f.Segments = append(f.Segments, newSegment)
	// Update the new segment size to encompass the program header table, which
	// we'll also append to the end of the file.
	programHeadersSize := uint32(binary.Size(f.Segments))
	f.Segments[len(f.Segments)-1].FileSize += programHeadersSize
	f.Segments[len(f.Segments)-1].MemorySize += programHeadersSize
	// Find the self-referential program header table segment, then update its
	// VA, offset, and size, too.
	for i := range f.Segments {
		if f.Segments[i].Type != elf_reader.ProgramHeaderSegment {
			continue
		}
		f.Segments[i].FileOffset = currentFileOffset
		f.Segments[i].VirtualAddress = currentVirtualAddress
		f.Segments[i].PhysicalAddress = 0
		f.Segments[i].FileSize = programHeadersSize
		f.Segments[i].MemorySize = programHeadersSize
		f.Segments[i].Align = 8
		break
	}
	// Write the updated program header table to the end of the file.
	e = writeAtELFOffset(f, currentFileOffset, f.Segments)
	if e != nil {
		return fmt.Errorf("Error writing updated program headers: %s", e)
	}
	// Update the ELF header to point to the new program header table. The
	// offset to the start of the table is at 28 bytes into the ELF header, and
	// the 2-byte number of entries is 44 bytes into the header.
	e = writeAtELFOffset(f, 28, currentFileOffset)
	if e != nil {
		return fmt.Errorf("Failed writing the program header table offset: %s",
			e)
	}
	programHeaderEntryCount := uint16(len(f.Segments))
	e = writeAtELFOffset(f, 44, programHeaderEntryCount)
	if e != nil {
		return fmt.Errorf("Failed writing the number of program header "+
			"entries: %s", e)
	}
	e = f.ReparseData()
	if e != nil {
		return fmt.Errorf("Error re-parsing ELF file after appending new "+
			"string tables: %s", e)
	}
	return nil
}

// Reads a 32-bit integer at the given offset in the ELF file. Returns an error
// if one occurs.
func readELFUint32(f *elf_reader.ELF32File, offset uint32) (uint32, error) {
	if (uint64(offset) + 3) > uint64(len(f.Raw)) {
		return 0, fmt.Errorf("Invalid offset for 32-bit value: %d", offset)
	}
	var toReturn uint32
	data := bytes.NewReader(f.Raw[offset:])
	e := binary.Read(data, f.Endianness, &toReturn)
	if e != nil {
		return 0, fmt.Errorf("Failed reading 32-bit value: %s", e)
	}
	return toReturn, nil
}

// Reads a 32-bit value the given offset in f.Raw, then uses this value as an
// offset into the replaced string table. If the string has been replaced, the
// 32-bit value in f.Raw will be replaced with a value pointing to the new
// string.
func replaceSingleOffset(f *elf_reader.ELF32File, offset uint32,
	replacedTable *replacedStringTable) error {
	value, e := readELFUint32(f, offset)
	if e != nil {
		return e
	}
	if uint64(value) > uint64(len(replacedTable.oldContent)) {
		return fmt.Errorf("Value at offset 0x%d in the file was invalid for "+
			"table %d", value, replacedTable.sectionIndex)
	}
	// Check this condition so we can at least know if the ELF file is doing
	// any funny business (replacing strings of this sort is ambiguous in the
	// current framework, so it won't occur).
	if (value != 0) && (replacedTable.oldContent[value-1] != 0) {
		s, e := elf_reader.ReadStringAtOffset(value, replacedTable.oldContent)
		if e != nil {
			s = []byte(fmt.Sprintf("<error reading string: %s>", e))
		}
		log.Printf("WARNING: String at offset %d in section %d (%s) doesn't "+
			"start immediately after the previous string.\n", value,
			replacedTable.sectionIndex, s)
	}
	for i, r := range replacedTable.replacements {
		if r.originalOffset != value {
			continue
		}
		e = writeAtELFOffset(f, offset, r.newOffset)
		if e != nil {
			return fmt.Errorf("Failed writing new string table offset: %s", e)
		}
		log.Printf("Replaced string reference at offset 0x%08x: %s\n", offset,
			replacedTable.showReplacement(i))
		break
	}
	return nil
}

// Returns a reference to the correct replacements table for the given section
// index, or nil if no replacements were made in the section.
func getReplacementTable(replacements []replacedStringTable,
	sectionIndex uint16) *replacedStringTable {
	var toReturn *replacedStringTable
	for i := range replacements {
		if replacements[i].sectionIndex != sectionIndex {
			continue
		}
		toReturn = &(replacements[i])
		break
	}
	return toReturn
}

// Replaces any section names that may have been changed
func replaceSectionNames(f *elf_reader.ELF32File,
	replacements []replacedStringTable) error {
	table := getReplacementTable(replacements, f.Header.SectionNamesTable)
	if table == nil {
		// No strings were replaced in the section names table.
		return nil
	}
	var baseOffset uint32
	var e error
	for i := range f.Sections {
		baseOffset = getSectionHeaderOffset(f, uint16(i))
		if e != nil {
			return fmt.Errorf("Failed finding section %d header: %s", i, e)
		}
		e = replaceSingleOffset(f, baseOffset, table)
		if e != nil {
			return fmt.Errorf("Failed replacing section %d name: %s", i, e)
		}
	}
	return nil
}

// Checks all symbol tables in the ELF file, and replaces the name field of
// each symbol as necessary.
func replaceSymbolNames(f *elf_reader.ELF32File,
	replacements []replacedStringTable) error {
	var e error
	var section *elf_reader.ELF32SectionHeader
	var table *replacedStringTable
	var currentSymbolOffset uint32
	symbolSize := uint32(binary.Size(&elf_reader.ELF32Symbol{}))
	// Loop through all symbol table sections
	for i := range f.Sections {
		if !f.IsSymbolTable(uint16(i)) {
			continue
		}
		section = &(f.Sections[i])
		table = getReplacementTable(replacements, uint16(section.LinkedIndex))
		if table == nil {
			continue
		}
		currentSymbolOffset = 0
		// Loop through all symbol definitions in individual sections
		for currentSymbolOffset < section.Size {
			// The name is the first field in the symbol structure.
			e = replaceSingleOffset(f, section.FileOffset+currentSymbolOffset,
				table)
			if e != nil {
				return fmt.Errorf("Failed replacing symbol name: %s", e)
			}
			currentSymbolOffset += symbolSize
		}
	}
	return nil
}

// Replaces names in the elf32_Verdaux structures, which are in turn referred
// to by elf32_Verdef structures. These are generally only used by shared
// library files to define symbol names.
func replaceVersionDefinitionStrings(f *elf_reader.ELF32File,
	replacements []replacedStringTable) error {
	// TODO: Implement replaceVersionDefinitionNames (also parse these sections
	// in elf_reader)
	return nil
}

// Replaces file and requirement names in the elf32_verneed and elf32_vernaux
// structures, from the .gnu_version_r section. This assumes that only one such
// section will be included in each ELF file.
func replaceVersionRequirementStrings(f *elf_reader.ELF32File,
	replacements []replacedStringTable) error {
	var section *elf_reader.ELF32SectionHeader
	var sectionIndex uint16
	for i := range f.Sections {
		if !f.IsVersionRequirementSection(uint16(i)) {
			continue
		}
		section = &(f.Sections[i])
		sectionIndex = uint16(i)
		break
	}
	// Do nothing if the file doesn't contain a GNU version requirement section
	if section == nil {
		return nil
	}
	table := getReplacementTable(replacements, uint16(section.LinkedIndex))
	// Do nothing if no strings were replaced in the section
	if table == nil {
		return nil
	}
	need, aux, e := f.ParseVersionRequirementSection(sectionIndex)
	if e != nil {
		return fmt.Errorf("Failed parsing version requirement section: %s", e)
	}
	currentNeedOffset := section.FileOffset
	var currentAuxOffset uint32
	// Loop through all elf32_verneed and associated elf32_vernaux structures
	// See the elf_reader package and
	// http://docs.oracle.com/cd/E19683-01/816-1386/chapter6-61174/index.html
	for i, n := range need {
		// The file name follows 2 2-byte fields in the structure
		e = replaceSingleOffset(f, currentNeedOffset+4, table)
		if e != nil {
			return fmt.Errorf("Failed replacing requirement file name: %s", e)
		}
		currentAuxOffset = currentNeedOffset + n.AuxOffset
		for _, x := range aux[i] {
			// The requirement name follows 1 4-byte and 2 2-byte fields
			e = replaceSingleOffset(f, currentAuxOffset+8, table)
			if e != nil {
				return fmt.Errorf("Failed replacing requirement name: %s", e)
			}
			currentAuxOffset += x.Next
		}
		currentNeedOffset += n.Next
	}
	return nil
}

// Replaces strings and the string table address in the dynamic linking table.
// Assumes that the file will only contain one dynamic linking table.
func replaceDynamicTableStrings(f *elf_reader.ELF32File,
	replacements []replacedStringTable) error {
	var sectionIndex uint16
	var section *elf_reader.ELF32SectionHeader
	for i := range f.Sections {
		if !f.IsDynamicSection(uint16(i)) {
			continue
		}
		sectionIndex = uint16(i)
		section = &(f.Sections[i])
		break
	}
	// Do nothing if the ELF didn't have a dynamic linking table.
	if section == nil {
		return nil
	}
	table := getReplacementTable(replacements, uint16(section.LinkedIndex))
	// Do nothing if no strings were replaced for this section.
	if table == nil {
		return nil
	}
	entries, e := f.GetDynamicTable(sectionIndex)
	if e != nil {
		return fmt.Errorf("Failed parsing dynamic table: %s", e)
	}
	currentOffset := section.FileOffset
	entrySize := uint32(binary.Size(&elf_reader.ELF32DynamicEntry{}))
	for _, entry := range entries {
		// Only tags 1, 14 and 15 have strings as values, as far as I know. Tag
		// 5 contains a string table address. The value field is 4 bytes from
		// the start of the table entry.
		switch entry.Tag {
		case 1, 14, 15:
			e = replaceSingleOffset(f, currentOffset+4, table)
			if e != nil {
				return fmt.Errorf("Failed replacing dynamic table string: %s",
					e)
			}
		case 5:
			e = writeAtELFOffset(f, currentOffset+4, table.newVirtualAddress)
			if e != nil {
				return fmt.Errorf(
					"Failed replacing dynamic table string table address: %s",
					e)
			}
		default:
		}
		currentOffset += entrySize
	}
	return nil
}

// Updates all known string table references in the ELF file to point to new
// string locations, if the referenced string was replaced. If this function
// returns an error, the ELF32File structure may be inconsistent, so an error
// should be treated as fatal to the entire procedure.
func updateStringReferences(f *elf_reader.ELF32File,
	replacements []replacedStringTable) error {
	log.Printf("Replacing section names.\n")
	e := replaceSectionNames(f, replacements)
	if e != nil {
		return fmt.Errorf("Failed replacing section names: %s", e)
	}
	log.Printf("Replacing symbol names.\n")
	e = replaceSymbolNames(f, replacements)
	if e != nil {
		return fmt.Errorf("Failed replacing symbol names: %s", e)
	}
	log.Printf("Replacing version definitions (stub: not supported).\n")
	e = replaceVersionDefinitionStrings(f, replacements)
	if e != nil {
		return fmt.Errorf("Failed replacing version definition strings: %s", e)
	}
	log.Printf("Replacing version requirements.\n")
	e = replaceVersionRequirementStrings(f, replacements)
	if e != nil {
		return fmt.Errorf("Failed replacing version req. strings: %s", e)
	}
	log.Printf("Replacing dynamic table strings.\n")
	e = replaceDynamicTableStrings(f, replacements)
	if e != nil {
		return fmt.Errorf("Failed replacing dynamic table strings: %s", e)
	}
	log.Printf("Sanity-checking result.\n")
	e = f.ReparseData()
	if e != nil {
		return fmt.Errorf("Failed re-parsing ELF post-string-replacement: %s",
			e)
	}
	return nil
}

func run() int {
	var inputFile, outputFile, matchRegex, replacement string
	flag.StringVar(&inputFile, "file", "", "The path to the input ELF file.")
	flag.StringVar(&outputFile, "output", "",
		"The name to give the modified ELF file.")
	flag.StringVar(&matchRegex, "to_match", "",
		"The regular expression to match in the string tables.")
	flag.StringVar(&replacement, "replace", "", "Matched string table entries"+
		" will be replaced with this. Supports referring to capture groups in"+
		" the regex using $<number>.")
	flag.Parse()
	if (inputFile == "") || (outputFile == "") || (matchRegex == "") ||
		(replacement == "") {
		log.Println("Invalid arguments. Run with -help for more information.")
		return 1
	}
	regex, e := regexp.Compile(matchRegex)
	if e != nil {
		log.Printf("Failed processing to_match regular expression: %s\n", e)
		return 1
	}
	rawInput, e := ioutil.ReadFile(inputFile)
	if e != nil {
		log.Printf("Failed reading input file: %s\n", e)
		return 1
	}
	elf, e := elf_reader.ParseELF32File(rawInput)
	if e != nil {
		log.Printf("Failed parsing the input file: %s\n", e)
		return 1
	}
	log.Printf("Parsed ELF file successfully.\n")
	// Finally, get to the meat of the operation... First, calculate new string
	// table content.
	replacements, e := processReplacements(elf, regex, replacement)
	if e != nil {
		log.Printf("Error performing string replacements: %s\n", e)
		return 1
	}
	// Second, append the new string tables to the end of the file, and update
	// necessary headers to the new locations.
	e = relocateStringTables(elf, replacements)
	if e != nil {
		log.Printf("Error relocating string tables: %s\n", e)
		return 1
	}
	// Third, update all of the string table references (now that the
	// replacements list has all the needed information).
	e = updateStringReferences(elf, replacements)
	if e != nil {
		log.Printf("Error updating string references: %s\n", e)
		return 1
	}
	// Finally output the new ELF file with updated strings.
	e = ioutil.WriteFile(outputFile, elf.Raw, 0755)
	if e != nil {
		log.Printf("Error creating output file: %s\n", e)
		return 1
	}
	return 0
}

func main() {
	log.SetFlags(0)
	log.SetOutput(os.Stdout)
	os.Exit(run())
}
