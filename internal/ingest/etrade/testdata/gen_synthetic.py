#!/usr/bin/env python3
"""Generate synthetic_vested_and_released.xlsx: FAKE DATA for the E*TRADE
Restricted Stock parser tests. Standard library only. Re-run after editing:

    python3 -I gen_synthetic.py

It uses the real export's column layout and adds the cases the anonymised
export lacks: vested, released and sellable shares, a fully cancelled grant,
an Excel serial-number date, a "Tax Withholding" record type (not imported,
so it should produce a warning), and a FAKE DATA preamble row above the header.
The released/tax column semantics here are assumptions, not observed data.
"""
import zipfile
from xml.sax.saxutils import escape

HEADER = ['Record Type', 'Symbol', 'Grant Date', 'Settlement Type', 'Granted Qty.', 'Withheld Qty.', 'Vested Qty.', 'Unvested Qty.', 'Deferred / Pending Release Qty.', 'Sellable Qty.', 'Est. Market Value', 'Grant Number', 'Cancelled Qty.', 'Pending Sale Qty.', 'Blocked Qty.', 'Type', 'Class', 'Status', 'Vest Period', 'Vest Date', 'Deferred Until', 'Granted Qty.', 'Other Reason for cancelled qty', 'Cancelled Qty.', 'Date Cancelled', 'Vested Qty.', 'Released Qty', 'Released Amount', 'Sellable Qty.', 'Blocked Qty.', 'Blocked Share Qty.', 'Total Taxes Paid', 'Sellable Qty.', 'Pending Sale Qty.', 'Sellable Est. Market Value', 'Est. Cost Basis (per share):', 'Est. Taxable Gain/Loss (per share):', 'Tax Status', 'Shares Traded for taxes', 'Tax Status', 'Tax Description', 'Taxable Gain', 'Effective Tax Rate', 'Withholding Amount', 'Total Estimated Tax Withholding', 'Dividend', 'Dividend Record Date', 'Dividend Payable Date', 'Dividend Cancel Date', 'Dividend Release Date', 'Dividend Type', 'Dividend Per Share', 'Dividend Market Value', 'Dividend Cash', 'Dividend Share', 'Release Dividend Shares', 'Dividend Fractional Shares Remaining', 'Dividend Market Value at Release', 'Cash-in-Lieu', 'Blocked', 'Blocked Type', 'Release Date', 'Accept By']

def col(i):
    s = ''
    i += 1
    while i:
        i, r = divmod(i - 1, 26)
        s = chr(65 + r) + s
    return s

def grant(**kw):
    """Grant row: values keyed by the FIRST occurrence of a header name."""
    row = [None] * len(HEADER)
    row[0] = 'Grant'
    for name, v in kw.items():
        row[HEADER.index(name)] = v
    return row

def vest(**kw):
    """Vest Schedule row: keyed by occurrence at/after 'Vest Period' (tax status = AN)."""
    row = [None] * len(HEADER)
    row[0] = 'Vest Schedule'
    vp = HEADER.index('Vest Period')
    for name, v in kw.items():
        if name == 'Grant Number':
            row[HEADER.index(name)] = v
        elif name == 'Tax Status':
            row[HEADER.index('Tax Status', HEADER.index('Shares Traded for taxes'))] = v
        else:
            row[HEADER.index(name, vp)] = v
    return row

rows = [
    ['FAKE DATA - synthetic E*TRADE Restricted Stock workbook for parser tests. Not a real export. See README.md.'],
    HEADER,
    # Grant 1: 100 granted, 50 vested (15 withheld for taxes, 35 sellable), 50 unvested. Price 200.
    grant(**{'Symbol': 'FAKE', 'Grant Date': '15-FEB-2024', 'Settlement Type': 'Stock', 'Granted Qty.': 100,
             'Withheld Qty.': 15, 'Vested Qty.': 50, 'Unvested Qty.': 50, 'Sellable Qty.': 35,
             'Est. Market Value': 17000, 'Grant Number': 'SYN-001', 'Cancelled Qty.': 0,
             'Type': 'RSU', 'Class': 'A', 'Status': 'ACCEPTED'}),
    # Tranche 1 vested on Excel serial 45704 (= 2025-02-16).
    vest(**{'Grant Number': 'SYN-001', 'Vest Period': 1, 'Vest Date': 45704, 'Granted Qty.': 50,
            'Cancelled Qty.': 0, 'Vested Qty.': 50, 'Released Qty': 35, 'Released Amount': 9000,
            'Total Taxes Paid': 3000, 'Shares Traded for taxes': 15, 'Tax Status': 'Paid'}),
    vest(**{'Grant Number': 'SYN-001', 'Vest Period': 2, 'Vest Date': '02/16/2027', 'Granted Qty.': 50,
            'Cancelled Qty.': 0, 'Vested Qty.': 0, 'Released Qty': 0, 'Released Amount': 0,
            'Total Taxes Paid': 0, 'Shares Traded for taxes': 0, 'Tax Status': 'Due at Vest'}),
    ['Tax Withholding', None, None, None, None, None, None, None, None, None, None, 'SYN-001'],
    # Grant 2: fully cancelled (e.g. forfeited).
    grant(**{'Symbol': 'FAKE', 'Grant Date': '14-FEB-2025', 'Settlement Type': 'Stock', 'Granted Qty.': 10,
             'Vested Qty.': 0, 'Unvested Qty.': 0, 'Sellable Qty.': 0, 'Est. Market Value': 0,
             'Grant Number': 'SYN-002', 'Cancelled Qty.': 10, 'Type': 'RSU', 'Class': 'A', 'Status': 'CANCELLED'}),
    vest(**{'Grant Number': 'SYN-002', 'Vest Period': 1, 'Vest Date': '02/16/2026', 'Granted Qty.': 10,
            'Cancelled Qty.': 10, 'Vested Qty.': 0, 'Released Qty': 0, 'Tax Status': 'Cancelled'}),
    ['Totals', None, None, None, None, 15, None, 50, None, 35, 17000],
]

def cell_xml(r, c, v):
    ref = f'{col(c)}{r}'
    if v is None:
        return ''
    if isinstance(v, (int, float)):
        return f'<c r="{ref}"><v>{v}</v></c>'
    return f'<c r="{ref}" t="inlineStr"><is><t>{escape(str(v))}</t></is></c>'

sheet_rows = ''.join(
    f'<row r="{i}">' + ''.join(cell_xml(i, c, v) for c, v in enumerate(row)) + '</row>'
    for i, row in enumerate(rows, start=1))

NS = 'xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"'
R = 'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"'
parts = {
    '[Content_Types].xml': '<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
        '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
        '<Default Extension="xml" ContentType="application/xml"/>'
        '<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>'
        '<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>',
    '_rels/.rels': '<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
        '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>',
    'xl/workbook.xml': f'<?xml version="1.0" encoding="UTF-8"?><workbook {NS} {R}><sheets><sheet name="Restricted Stock" sheetId="1" r:id="rId1"/></sheets></workbook>',
    'xl/_rels/workbook.xml.rels': '<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
        '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>',
    'xl/worksheets/sheet1.xml': f'<?xml version="1.0" encoding="UTF-8"?><worksheet {NS}><sheetData>{sheet_rows}</sheetData></worksheet>',
}

with zipfile.ZipFile('synthetic_vested_and_released.xlsx', 'w', zipfile.ZIP_DEFLATED) as z:
    for name, body in parts.items():
        # Fixed timestamp so regenerating produces identical bytes.
        info = zipfile.ZipInfo(name, date_time=(2026, 1, 1, 0, 0, 0))
        info.compress_type = zipfile.ZIP_DEFLATED
        z.writestr(info, body)
print('wrote synthetic_vested_and_released.xlsx')
