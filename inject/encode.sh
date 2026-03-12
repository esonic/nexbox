#!/bin/sh
# encode.sh — XOR-encode a token for use in inject.c
#
# Usage: ./encode.sh <token>
#
# Output: C array initializer to paste into inject.c enc_token[]
# Key must match XOR_KEY in inject.c (currently 0x5A)

set -e

XOR_KEY=90  # 0x5A in decimal

if [ -z "$1" ]; then
    echo "Usage: $0 <token>" >&2
    exit 1
fi

TOKEN="$1"
LEN=${#TOKEN}

printf "static const unsigned char enc_token[] = {\n    "

i=0
while [ $i -lt $LEN ]; do
    # Extract one character and get its ASCII value
    CHAR=$(printf '%s' "$TOKEN" | cut -c$((i+1)))
    ORD=$(printf '%d' "'$CHAR")
    XOR=$(( ORD ^ XOR_KEY ))
    printf "0x%02x" $XOR
    i=$((i+1))
    if [ $i -lt $LEN ]; then
        printf ", "
        # Wrap at 8 per line
        if [ $((i % 8)) -eq 0 ]; then
            printf "\n    "
        fi
    fi
done

printf "\n};\n"
