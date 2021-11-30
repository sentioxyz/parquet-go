package parquet_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"github.com/segmentio/parquet"
)

func scanParquetFile(f *os.File) error {
	s, err := f.Stat()
	if err != nil {
		return err
	}

	p, err := parquet.OpenFile(f, s.Size())
	if err != nil {
		return err
	}

	return scanParquetColumns(p.Root())
}

func scanParquetColumns(col *parquet.Column) error {
	const bufferSize = 1024
	chunks := col.Chunks()

	for chunks.Next() {
		pages := chunks.Pages()
		dictionary := (parquet.Dictionary)(nil)

		for pages.Next() {
			switch header := pages.PageHeader().(type) {
			case parquet.DictionaryPageHeader:
				decoder := header.Encoding().NewDecoder(pages.PageData())
				dictionary = col.Type().NewDictionary(0)

				if err := dictionary.ReadFrom(decoder); err != nil {
					return err
				}

			case parquet.DataPageHeader:
				var pageReader parquet.PageReader
				var pageData = header.Encoding().NewDecoder(pages.PageData())

				if dictionary != nil {
					pageReader = parquet.NewIndexedPageReader(pageData, bufferSize, dictionary)
				} else {
					pageReader = col.Type().NewPageReader(pageData, bufferSize)
				}

				dataPageReader := parquet.NewDataPageReader(
					header.RepetitionLevelEncoding().NewDecoder(pages.RepetitionLevels()),
					header.DefinitionLevelEncoding().NewDecoder(pages.DefinitionLevels()),
					header.NumValues(),
					pageReader,
					col.MaxRepetitionLevel(),
					col.MaxDefinitionLevel(),
					bufferSize,
				)

				for {
					v, err := dataPageReader.ReadValue()
					if err != nil {
						if err != io.EOF {
							return err
						}
						break
					}
					fmt.Printf("> %+v\n", v)
				}

			default:
				return fmt.Errorf("unsupported page header type: %#v", header)
			}

			if err := pages.Err(); err != nil {
				return err
			}
		}
	}

	for _, child := range col.Columns() {
		if err := scanParquetColumns(child); err != nil {
			return err
		}
	}

	return nil
}

func generateParquetFile(dataPageVersion int, rows ...interface{}) ([]byte, error) {
	tmp, err := os.CreateTemp("/tmp", "*.parquet")
	if err != nil {
		return nil, err
	}
	defer tmp.Close()
	path := tmp.Name()
	defer os.Remove(path)

	schema := parquet.SchemaOf(rows[0])
	writer := parquet.NewWriter(tmp, schema, parquet.DataPageVersion(dataPageVersion))

	for _, row := range rows {
		if err := writer.WriteRow(row); err != nil {
			return nil, err
		}
	}

	if err := writer.Close(); err != nil {
		return nil, err
	}

	if err := scanParquetFile(tmp); err != nil {
		return nil, err
	}

	return parquetTools("dump", path)
}

type firstAndLastName struct {
	FirstName string `parquet:"first_name,dict"`
	LastName  string `parquet:"last_name,dict"`
}

var writerTests = []struct {
	version int
	rows    []interface{}
	dump    string
}{
	{
		version: 1,
		rows: []interface{}{
			&firstAndLastName{FirstName: "Han", LastName: "Solo"},
			&firstAndLastName{FirstName: "Leia", LastName: "Skywalker"},
			&firstAndLastName{FirstName: "Luke", LastName: "Skywalker"},
		},
		dump: `row group 0
--------------------------------------------------------------------------------
first_name:  BINARY UNCOMPRESSED DO:4 FPO:46 SZ:96/96/1.00 VC:3 ENC:PL [more]...
last_name:   BINARY UNCOMPRESSED DO:100 FPO:140 SZ:104/104/1.00 VC:3 E [more]...

    first_name TV=3 RL=0 DL=0 DS: 3 DE:PLAIN
    ----------------------------------------------------------------------------
    page 0:                        DLE:RLE RLE:RLE VLE:RLE_DICTIONARY  [more]... SZ:7

    last_name TV=3 RL=0 DL=0 DS:  2 DE:PLAIN
    ----------------------------------------------------------------------------
    page 0:                        DLE:RLE RLE:RLE VLE:RLE_DICTIONARY  [more]... SZ:5

BINARY first_name
--------------------------------------------------------------------------------
*** row group 1 of 1, values 1 to 3 ***
value 1: R:0 D:0 V:Han
value 2: R:0 D:0 V:Leia
value 3: R:0 D:0 V:Luke

BINARY last_name
--------------------------------------------------------------------------------
*** row group 1 of 1, values 1 to 3 ***
value 1: R:0 D:0 V:Solo
value 2: R:0 D:0 V:Skywalker
value 3: R:0 D:0 V:Skywalker
`,
	},

	{
		version: 2,
		rows: []interface{}{
			AddressBook{
				Owner: "Julien Le Dem",
				OwnerPhoneNumbers: []string{
					"555 123 4567",
					"555 666 1337",
				},
				Contacts: []Contact{
					{
						Name:        "Dmitriy Ryaboy",
						PhoneNumber: "555 987 6543",
					},
					{
						Name: "Chris Aniszczyk",
					},
				},
			},
			AddressBook{
				Owner:             "A. Nonymous",
				OwnerPhoneNumbers: nil,
			},
		},

		dump: `row group 0
--------------------------------------------------------------------------------
contacts:
.name:              BINARY UNCOMPRESSED DO:0 FPO:4 SZ:145/145/1.00 VC:3 [more]...
.phoneNumber:       BINARY SNAPPY DO:0 FPO:149 SZ:118/116/0.98 VC:3 EN [more]...
owner:              BINARY ZSTD DO:0 FPO:267 SZ:127/118/0.93 VC:2 ENC:PLAIN,RLE [more]...
ownerPhoneNumbers:  BINARY GZIP DO:0 FPO:394 SZ:155/130/0.84 VC:3 ENC:PLAIN,RLE [more]...

    contacts.name TV=3 RL=1 DL=1
    ----------------------------------------------------------------------------
    page 0:  DLE:RLE RLE:RLE VLE:PLAIN ST:[min: Chris Aniszczyk, max:  [more]... VC:3

    contacts.phoneNumber TV=3 RL=1 DL=2
    ----------------------------------------------------------------------------
    page 0:  DLE:RLE RLE:RLE VLE:PLAIN ST:[min: 555 987 6543, max: 555 [more]... VC:3

    owner TV=2 RL=0 DL=0
    ----------------------------------------------------------------------------
    page 0:  DLE:RLE RLE:RLE VLE:PLAIN ST:[min: A. Nonymous, max: Juli [more]... VC:2

    ownerPhoneNumbers TV=3 RL=1 DL=1
    ----------------------------------------------------------------------------
    page 0:  DLE:RLE RLE:RLE VLE:PLAIN ST:[min: 555 123 4567, max: 555 [more]... VC:3

BINARY contacts.name
--------------------------------------------------------------------------------
*** row group 1 of 1, values 1 to 3 ***
value 1: R:0 D:1 V:Dmitriy Ryaboy
value 2: R:1 D:1 V:Chris Aniszczyk
value 3: R:0 D:0 V:<null>

BINARY contacts.phoneNumber
--------------------------------------------------------------------------------
*** row group 1 of 1, values 1 to 3 ***
value 1: R:0 D:2 V:555 987 6543
value 2: R:1 D:1 V:<null>
value 3: R:0 D:0 V:<null>

BINARY owner
--------------------------------------------------------------------------------
*** row group 1 of 1, values 1 to 2 ***
value 1: R:0 D:0 V:Julien Le Dem
value 2: R:0 D:0 V:A. Nonymous

BINARY ownerPhoneNumbers
--------------------------------------------------------------------------------
*** row group 1 of 1, values 1 to 3 ***
value 1: R:0 D:1 V:555 123 4567
value 2: R:1 D:1 V:555 666 1337
value 3: R:0 D:0 V:<null>
`,
	},
}

func TestWriter(t *testing.T) {
	if !hasParquetTools() {
		t.Skip("parquet-tools are not installed")
	}

	for _, test := range writerTests {
		dataPageVersion := test.version
		rows := test.rows
		dump := test.dump

		t.Run("", func(t *testing.T) {
			t.Parallel()

			b, err := generateParquetFile(dataPageVersion, rows...)
			if err != nil {
				t.Logf("\n%s", string(b))
				t.Fatal(err)
			}

			if string(b) != dump {
				edits := myers.ComputeEdits(span.URIFromPath("want.txt"), dump, string(b))
				diff := fmt.Sprint(gotextdiff.ToUnified("want.txt", "got.txt", dump, edits))
				t.Errorf("\n%s", diff)
			}
		})
	}
}

func hasParquetTools() bool {
	_, err := exec.LookPath("parquet-tools")
	return err == nil
}

func parquetTools(cmd, path string) ([]byte, error) {
	p := exec.Command("parquet-tools", cmd, "--debug", path)

	output, err := p.CombinedOutput()
	if err != nil {
		return output, err
	}

	// parquet-tools has trailing spaces on some lines
	lines := bytes.Split(output, []byte("\n"))

	for i, line := range lines {
		lines[i] = bytes.TrimRight(line, " ")
	}

	return bytes.Join(lines, []byte("\n")), nil
}