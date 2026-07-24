import { TextInput } from "@mantine/core";
import type { TextInputProps } from "@mantine/core";

// Re-export Mantine TextInput with size="sm" as default
// The MantineProvider already sets size="sm" as default
export default TextInput;
export { TextInput };
export type { TextInputProps };
