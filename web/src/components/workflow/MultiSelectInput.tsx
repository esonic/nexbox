import { useState, useRef, useEffect } from 'react';
import { X } from 'lucide-react';

interface MultiSelectInputProps {
  items: Array<{ id: string; title: string }>;
  selectedIds: string[];
  onSelect: (id: string) => void;
  onRemove: (id: string) => void;
  placeholder: string;
  itemType: 'task' | 'asset';
}

export default function MultiSelectInput({
  items,
  selectedIds,
  onSelect,
  onRemove,
  placeholder,
  itemType,
}: MultiSelectInputProps) {
  const [inputValue, setInputValue] = useState('');
  const [showDropdown, setShowDropdown] = useState(false);
  const [focusedIndex, setFocusedIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);

  // Filter items based on input
  const filteredItems = items.filter(
    (item) =>
      !selectedIds.includes(item.id) &&
      (inputValue === '' || item.title.toLowerCase().includes(inputValue.toLowerCase()))
  );

  // Handle input change
  const handleInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const value = e.target.value;
    setInputValue(value);
    setShowDropdown(true);
    setFocusedIndex(0);
  };

  // Handle item selection
  const handleSelect = (id: string) => {
    onSelect(id);
    setInputValue('');
    setShowDropdown(false);
    inputRef.current?.focus();
  };

  // Handle keyboard navigation
  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (!showDropdown) return;

    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        setFocusedIndex((prev) => Math.min(prev + 1, filteredItems.length - 1));
        break;
      case 'ArrowUp':
        e.preventDefault();
        setFocusedIndex((prev) => Math.max(prev - 1, 0));
        break;
      case 'Enter':
        e.preventDefault();
        if (filteredItems[focusedIndex]) {
          handleSelect(filteredItems[focusedIndex].id);
        }
        break;
      case 'Escape':
        e.preventDefault();
        setShowDropdown(false);
        setInputValue('');
        break;
      case 'Backspace':
        if (inputValue === '' && selectedIds.length > 0) {
          onRemove(selectedIds[selectedIds.length - 1]);
        }
        break;
    }
  };

  // Click outside to close
  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (
        dropdownRef.current &&
        !dropdownRef.current.contains(e.target as Node) &&
        !inputRef.current?.contains(e.target as Node)
      ) {
        setShowDropdown(false);
      }
    };

    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  // Scroll focused item into view
  useEffect(() => {
    if (showDropdown && dropdownRef.current) {
      const focusedElement = dropdownRef.current.children[focusedIndex] as HTMLElement;
      focusedElement?.scrollIntoView({ block: 'nearest' });
    }
  }, [focusedIndex, showDropdown]);

  const selectedItems = items.filter((item) => selectedIds.includes(item.id));
  const tagColor = itemType === 'task'
    ? 'bg-blue-100 dark:bg-blue-900/30 text-blue-700 dark:text-blue-400'
    : 'bg-green-100 dark:bg-green-900/30 text-green-700 dark:text-green-400';

  return (
    <div className="relative">
      <div className="w-full min-h-[28px] px-2 py-1 border border-border/30 rounded text-[11px] bg-background flex flex-wrap gap-1 items-center">
        {/* Selected tags */}
        {selectedIds.map((id) => {
          const item = items.find((item) => item.id === id);
          if (!item) return null;
          return (
            <div
              key={item.id}
              className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium ${tagColor}`}
            >
              <span className="truncate max-w-[120px]">{item.title}</span>
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  onRemove(item.id);
                }}
                className="hover:opacity-70"
              >
                <X className="w-2.5 h-2.5" />
              </button>
            </div>
          );
        })}

        {/* Input */}
        <input
          ref={inputRef}
          type="text"
          value={inputValue}
          onChange={handleInputChange}
          onKeyDown={handleKeyDown}
          onFocus={() => {
            setShowDropdown(true);
            setFocusedIndex(0);
          }}
          onClick={() => {
            setShowDropdown(true);
            setFocusedIndex(0);
          }}
          placeholder={selectedIds.length === 0 ? placeholder : ''}
          className="flex-1 min-w-[60px] outline-none bg-transparent text-[11px]"
        />
      </div>

      {/* Dropdown */}
      {showDropdown && filteredItems.length > 0 && (
        <div
          ref={dropdownRef}
          className="absolute z-50 w-full mt-1 max-h-[200px] overflow-y-auto bg-background border border-border/30 rounded shadow-lg"
        >
          {filteredItems.map((item, index) => (
            <div
              key={item.id}
              onClick={() => handleSelect(item.id)}
              className={`px-2 py-1.5 text-[11px] cursor-pointer ${
                index === focusedIndex
                  ? 'bg-accent/50'
                  : 'hover:bg-accent/30'
              }`}
            >
              <div className="font-medium">{item.title}</div>
              <div className="text-[9px] text-muted-foreground truncate">{item.id}</div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
