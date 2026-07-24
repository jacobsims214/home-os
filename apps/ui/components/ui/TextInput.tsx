import { TextInput as MantineTextInput, TextInputProps } from "@mantine/core";

export function TextInput({ ...props }: TextInputProps) {
  return <MantineTextInput {...props} />;
}

export default TextInput;
